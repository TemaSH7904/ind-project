package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

type ImportResponse struct {
	TotalCount      int   `json:"total_count"`
	DuplicatesCount int   `json:"duplicates_count"`
	TotalItems      int   `json:"total_items"`
	TotalCategories int   `json:"total_categories"`
	TotalPrice      int64 `json:"total_price"`
}

type PriceRow struct {
	Name       string
	Category   string
	Price      int64
	CreateDate time.Time
}

func main() {
	if err := run(); err != nil {
		log.Printf("server stopped with error: %v", err)
		os.Exit(1) // defer внутри run() уже отработал
	}
}

func run() error {
	d, err := initDB()
	if err != nil {
		return err
	}
	db = d
	defer func() { _ = db.Close() }()

	http.HandleFunc("/api/v0/prices", handlePrices)

	log.Println("🚀 Сервер запущен на порту 8080")
	return http.ListenAndServe(":8080", nil)
}

func getenv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func initDB() (*sql.DB, error) {
	// дефолты под требования проекта/автотесты
	host := getenv("DB_HOST", "localhost")
	port := getenv("DB_PORT", "5432")
	user := getenv("DB_USER", "validator")
	pass := getenv("DB_PASSWORD", "val1dat0r")
	name := getenv("DB_NAME", "project-sem-1")

	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, name,
	)

	d, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}
	if err := d.Ping(); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("нет соединения с БД: %w", err)
	}

	log.Println("✅ Успешное подключение к базе данных!")
	return d, nil
}

func handlePrices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handlePost(w, r)
	case http.MethodGet:
		handleGet(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handlePost(w http.ResponseWriter, r *http.Request) {
	archiveType := r.URL.Query().Get("type")
	if archiveType == "" {
		archiveType = "zip"
	}

	// автотесты отправляют multipart/form-data: curl -F "file=@..."
	fileBytes, err := readMultipartFile(r, "file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	csvReader, closeFn, err := csvReaderFromArchive(archiveType, fileBytes)
	if closeFn != nil {
		defer closeFn()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := importCSVWithValidation(r.Context(), csvReader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func readMultipartFile(r *http.Request, field string) ([]byte, error) {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil, fmt.Errorf("missing form file %q", field)
	}
	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read uploaded file: %w", err)
	}
	if len(b) == 0 {
		return nil, errors.New("uploaded file is empty")
	}
	return b, nil
}

func csvReaderFromArchive(archiveType string, fileContent []byte) (*csv.Reader, func(), error) {
	switch archiveType {
	case "zip":
		zr, err := zip.NewReader(bytes.NewReader(fileContent), int64(len(fileContent)))
		if err != nil {
			return nil, nil, errors.New("invalid zip")
		}
		for _, f := range zr.File {
			if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
				rc, err := f.Open()
				if err != nil {
					return nil, nil, fmt.Errorf("failed to open csv in zip: %w", err)
				}
				return csv.NewReader(rc), func() { _ = rc.Close() }, nil
			}
		}
		return nil, nil, errors.New("csv file not found in zip")

	case "tar":
		tr := tar.NewReader(bytes.NewReader(fileContent))
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, nil, fmt.Errorf("invalid tar: %w", err)
			}
			if strings.HasSuffix(strings.ToLower(h.Name), ".csv") {
				return csv.NewReader(tr), nil, nil
			}
		}
		return nil, nil, errors.New("csv file not found in tar")

	default:
		return nil, nil, errors.New("unsupported archive type")
	}
}

// 1) читаем весь CSV 2) валидируем 3) дедуплим в рамках файла 4) пишем в БД транзакцией
func importCSVWithValidation(ctx context.Context, reader *csv.Reader) (ImportResponse, error) {
	var resp ImportResponse

	rows, totalCount, dupInFile, err := readAndValidateAll(reader)
	if err != nil {
		return resp, err
	}
	resp.TotalCount = totalCount
	resp.DuplicatesCount = dupInFile

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return resp, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO prices (name, category, price, create_date)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (name, category, price, create_date) DO NOTHING
	`)
	if err != nil {
		return resp, err
	}
	defer func() { _ = stmt.Close() }()

	for _, pr := range rows {
		res, err := stmt.ExecContext(ctx, pr.Name, pr.Category, pr.Price, pr.CreateDate.Format("2006-01-02"))
		if err != nil {
			return resp, err
		}
		ra, err := res.RowsAffected()
		if err != nil {
			return resp, err
		}
		if ra > 0 {
			resp.TotalItems++
		} else {
			// дубликат в БД (по всем полям кроме id)
			resp.DuplicatesCount++
		}
	}

	// статистика ДО коммита транзакции
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT category) AS total_categories,
			COALESCE(SUM(price), 0)  AS total_price
		FROM prices
	`).Scan(&resp.TotalCategories, &resp.TotalPrice); err != nil {
		return resp, err
	}

	if err := tx.Commit(); err != nil {
		return resp, err
	}
	committed = true

	return resp, nil
}

func readAndValidateAll(reader *csv.Reader) ([]PriceRow, int, int, error) {
	var (
		totalCount int
		dupInFile  int
		validRows  []PriceRow
		seen       = make(map[string]struct{})
	)

	first, err := reader.Read()
	if err == io.EOF {
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, err
	}

	process := func(rec []string) {
		totalCount++

		pr, ok := parseRecord(rec)
		if !ok {
			return
		}

		key := dedupKey(pr)
		if _, exists := seen[key]; exists {
			dupInFile++
			return
		}
		seen[key] = struct{}{}
		validRows = append(validRows, pr)
	}

	// если первая строка — заголовок, не считаем её как data-строку
	if !isHeader(first) {
		process(first)
	}

	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, 0, err
		}
		process(rec)
	}

	return validRows, totalCount, dupInFile, nil
}

func isHeader(rec []string) bool {
	if len(rec) != 5 {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(rec[0]))
	b := strings.ToLower(strings.TrimSpace(rec[1]))
	c := strings.ToLower(strings.TrimSpace(rec[2]))
	d := strings.ToLower(strings.TrimSpace(rec[3]))
	e := strings.ToLower(strings.TrimSpace(rec[4]))
	return a == "id" && b == "name" && c == "category" && d == "price" && e == "create_date"
}

func parseRecord(record []string) (PriceRow, bool) {
	if len(record) != 5 {
		return PriceRow{}, false
	}

	// id валидируем, но в БД НЕ вставляем (там автоинкремент)
	if _, err := strconv.ParseInt(strings.TrimSpace(record[0]), 10, 64); err != nil {
		return PriceRow{}, false
	}

	name := strings.TrimSpace(record[1])
	category := strings.TrimSpace(record[2])
	if name == "" || category == "" {
		return PriceRow{}, false
	}

	price, err := strconv.ParseInt(strings.TrimSpace(record[3]), 10, 64)
	if err != nil || price <= 0 {
		return PriceRow{}, false
	}

	dt, err := time.Parse("2006-01-02", strings.TrimSpace(record[4]))
	if err != nil {
		return PriceRow{}, false
	}

	return PriceRow{
		Name:       name,
		Category:   category,
		Price:      price,
		CreateDate: dt,
	}, true
}

func dedupKey(p PriceRow) string {
	// дубликат = совпадает всё, кроме id
	return fmt.Sprintf("%s\x1f%s\x1f%d\x1f%s",
		p.Name, p.Category, p.Price, p.CreateDate.Format("2006-01-02"),
	)
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	minStr := r.URL.Query().Get("min")
	maxStr := r.URL.Query().Get("max")

	query := "SELECT id, name, category, price, create_date FROM prices WHERE 1=1"
	args := []interface{}{}
	argId := 1

	if startStr != "" {
		if _, err := time.Parse("2006-01-02", startStr); err != nil {
			http.Error(w, "invalid start date", http.StatusBadRequest)
			return
		}
		query += fmt.Sprintf(" AND create_date >= $%d", argId)
		args = append(args, startStr)
		argId++
	}

	if endStr != "" {
		if _, err := time.Parse("2006-01-02", endStr); err != nil {
			http.Error(w, "invalid end date", http.StatusBadRequest)
			return
		}
		query += fmt.Sprintf(" AND create_date <= $%d", argId)
		args = append(args, endStr)
		argId++
	}

	if minStr != "" {
		minP, err := strconv.ParseInt(minStr, 10, 64)
		if err != nil || minP <= 0 {
			http.Error(w, "invalid min price", http.StatusBadRequest)
			return
		}
		query += fmt.Sprintf(" AND price >= $%d", argId)
		args = append(args, minP)
		argId++
	}

	if maxStr != "" {
		maxP, err := strconv.ParseInt(maxStr, 10, 64)
		if err != nil || maxP <= 0 {
			http.Error(w, "invalid max price", http.StatusBadRequest)
			return
		}
		query += fmt.Sprintf(" AND price <= $%d", argId)
		args = append(args, maxP)
		argId++
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, "db query error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	csvFile, err := zipWriter.Create("data.csv")
	if err != nil {
		_ = zipWriter.Close()
		http.Error(w, "failed to create csv in zip", http.StatusInternalServerError)
		return
	}

	csvWriter := csv.NewWriter(csvFile)
	if err := csvWriter.Write([]string{"id", "name", "category", "price", "create_date"}); err != nil {
		csvWriter.Flush()
		_ = zipWriter.Close()
		http.Error(w, "failed to write csv header", http.StatusInternalServerError)
		return
	}

	for rows.Next() {
		var (
			id         int64
			name       string
			category   string
			price      int64
			createDate time.Time
		)

		if err := rows.Scan(&id, &name, &category, &price, &createDate); err != nil {
			csvWriter.Flush()
			_ = zipWriter.Close()
			http.Error(w, "db scan error", http.StatusInternalServerError)
			return
		}

		if err := csvWriter.Write([]string{
			strconv.FormatInt(id, 10),
			name,
			category,
			strconv.FormatInt(price, 10),
			createDate.Format("2006-01-02"),
		}); err != nil {
			csvWriter.Flush()
			_ = zipWriter.Close()
			http.Error(w, "failed to write csv row", http.StatusInternalServerError)
			return
		}
	}

	// важно: обработать ошибку итерации rows
	if err := rows.Err(); err != nil {
		csvWriter.Flush()
		_ = zipWriter.Close()
		http.Error(w, "db rows error", http.StatusInternalServerError)
		return
	}

	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		_ = zipWriter.Close()
		http.Error(w, "csv flush error", http.StatusInternalServerError)
		return
	}

	if err := zipWriter.Close(); err != nil {
		http.Error(w, "zip close error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=data.zip")
	_, _ = w.Write(buf.Bytes())
}