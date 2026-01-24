package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
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

func main() {
	initDB()
	defer db.Close()

	http.HandleFunc("/api/v0/prices", handlePrices)

	log.Println("🚀 Сервер запущен на порту 8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func initDB() {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_NAME"))

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}

	if err = db.Ping(); err != nil {
		log.Fatal("Нет соединения с БД: ", err)
	}
	log.Println("✅ Успешное подключение к базе данных!")
}

func handlePrices(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		handlePost(w, r)
	} else if r.Method == http.MethodGet {
		handleGet(w, r)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handlePost(w http.ResponseWriter, r *http.Request) {
	archiveType := r.URL.Query().Get("type")
	if archiveType == "" {
		archiveType = "zip"
	}

	fileContent, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var csvReader *csv.Reader

	if archiveType == "zip" {
		zipReader, err := zip.NewReader(bytes.NewReader(fileContent), int64(len(fileContent)))
		if err != nil {
			http.Error(w, "Invalid zip", http.StatusBadRequest)
			return
		}
		for _, file := range zipReader.File {
			if strings.HasSuffix(file.Name, ".csv") {
				f, _ := file.Open()
				csvReader = csv.NewReader(f)
				break
			}
		}
	} else if archiveType == "tar" {
		tarReader := tar.NewReader(bytes.NewReader(fileContent))
		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			}
			if strings.HasSuffix(header.Name, ".csv") {
				csvReader = csv.NewReader(tarReader)
				break
			}
		}
	} else {
		http.Error(w, "Unsupported archive type", http.StatusBadRequest)
		return
	}

	if csvReader == nil {
		http.Error(w, "data.csv not found in archive", http.StatusBadRequest)
		return
	}

	stats := processCSV(csvReader)

	// Статистика из БД
	stats.TotalCategories = countCategories()
	stats.TotalPrice = countTotalPrice()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func processCSV(reader *csv.Reader) ImportResponse {
	var resp ImportResponse

	// Читаем заголовок
	_, err := reader.Read()
	if err != nil {
		return resp
	}

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		resp.TotalCount++

		if len(record) != 5 {
			continue
		}

		// ИСПРАВЛЕННЫЙ ПАРСИНГ (под tests.sh)
		// CSV: id, name, category, price, create_date
		id, err1 := strconv.ParseInt(record[0], 10, 64)
		// record[1] - name
		// record[2] - category
		price, err2 := strconv.ParseInt(record[3], 10, 64) // Цена теперь индекс 3
		dateStr := record[4]                               // Дата теперь индекс 4

		_, err3 := time.Parse("2006-01-02", dateStr)

		if err1 != nil || err2 != nil || err3 != nil || price <= 0 || record[1] == "" || record[2] == "" {
			continue
		}

		inserted := insertPrice(id, dateStr, record[1], record[2], price)
		if inserted {
			resp.TotalItems++
		} else {
			resp.DuplicatesCount++
		}
	}
	return resp
}

func insertPrice(id int64, date, name, category string, price int64) bool {
	// Исправлено имя колонки на create_date
	query := `INSERT INTO prices (id, name, category, price, create_date) 
              VALUES ($1, $2, $3, $4, $5) 
              ON CONFLICT (id) DO NOTHING`
	res, err := db.Exec(query, id, name, category, price, date)
	if err != nil {
		log.Println("Insert error:", err)
		return false
	}
	rows, _ := res.RowsAffected()
	return rows > 0
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	minPrice := r.URL.Query().Get("min")
	maxPrice := r.URL.Query().Get("max")

	// Исправлено имя колонки в SELECT и WHERE
	query := "SELECT id, name, category, price, to_char(create_date, 'YYYY-MM-DD') FROM prices WHERE 1=1"
	args := []interface{}{}
	argId := 1

	if start != "" {
		query += fmt.Sprintf(" AND create_date >= $%d", argId)
		args = append(args, start)
		argId++
	}
	if end != "" {
		query += fmt.Sprintf(" AND create_date <= $%d", argId)
		args = append(args, end)
		argId++
	}
	if minPrice != "" {
		query += fmt.Sprintf(" AND price >= $%d", argId)
		args = append(args, minPrice)
		argId++
	}
	if maxPrice != "" {
		query += fmt.Sprintf(" AND price <= $%d", argId)
		args = append(args, maxPrice)
		argId++
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)
	csvFile, _ := zipWriter.Create("data.csv")
	csvWriter := csv.NewWriter(csvFile)

	// Заголовок CSV (под требования тестов)
	csvWriter.Write([]string{"id", "name", "category", "price", "create_date"})

	for rows.Next() {
		var id int64
		var date, name, category string
		var price int64
		// Сканируем в правильном порядке SELECT
		if err := rows.Scan(&id, &name, &category, &price, &date); err == nil {
			csvWriter.Write([]string{
				fmt.Sprintf("%d", id),
				name,
				category,
				fmt.Sprintf("%d", price),
				date,
			})
		}
	}
	csvWriter.Flush()
	zipWriter.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=data.zip")
	w.Write(buf.Bytes())
}

func countCategories() int {
	var count int
	db.QueryRow("SELECT COUNT(DISTINCT category) FROM prices").Scan(&count)
	return count
}

func countTotalPrice() int64 {
	var total int64
	db.QueryRow("SELECT COALESCE(SUM(price), 0) FROM prices").Scan(&total)
	return total
}