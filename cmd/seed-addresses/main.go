// kakitu_wallet_server/cmd/seed-addresses/main.go
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"

	"github.com/kakitucurrency/kakitu-wallet-server/database"
	"github.com/kakitucurrency/kakitu-wallet-server/models/dbmodels"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func main() {
	csvPath := flag.String("csv", "", "Path to CSV file with columns: line1,city,country,postal_code")
	batchSize := flag.Int("batch", 500, "Insert batch size")
	flag.Parse()

	if *csvPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: seed-addresses -csv /path/to/addresses.csv")
		os.Exit(1)
	}

	db, err := database.NewConnection(&database.Config{
		URL:      os.Getenv("DATABASE_URL"),
		Host:     os.Getenv("DB_HOST"),
		Port:     os.Getenv("DB_PORT"),
		User:     os.Getenv("DB_USER"),
		Password: os.Getenv("DB_PASS"),
		DBName:   os.Getenv("DB_NAME"),
		SSLMode:  os.Getenv("DB_SSLMODE"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "DB connection failed: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Open(*csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open CSV: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "CSV parse error: %v\n", err)
		os.Exit(1)
	}

	if len(rows) < 2 {
		fmt.Fprintln(os.Stderr, "CSV must have header + at least 1 data row")
		os.Exit(1)
	}

	var batch []dbmodels.StripeAddress
	inserted := 0
	skipped := 0

	for _, row := range rows[1:] { // skip header
		if len(row) < 4 {
			skipped++
			continue
		}
		batch = append(batch, dbmodels.StripeAddress{
			Line1:      row[0],
			City:       row[1],
			Country:    row[2],
			PostalCode: row[3],
		})

		if len(batch) >= *batchSize {
			if err := insertBatch(db, batch); err != nil {
				fmt.Fprintf(os.Stderr, "Insert error: %v\n", err)
				os.Exit(1)
			}
			inserted += len(batch)
			batch = batch[:0]
			fmt.Printf("Inserted %d rows...\n", inserted)
		}
	}

	if len(batch) > 0 {
		if err := insertBatch(db, batch); err != nil {
			fmt.Fprintf(os.Stderr, "Insert error: %v\n", err)
			os.Exit(1)
		}
		inserted += len(batch)
	}

	fmt.Printf("Done. Inserted: %d, Skipped (malformed): %d\n", inserted, skipped)
}

func insertBatch(db *gorm.DB, batch []dbmodels.StripeAddress) error {
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch).Error
}
