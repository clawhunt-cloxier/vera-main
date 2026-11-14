// Command dbmigrate runs River + goose migrations against DATABASE_URL.
// Unlike "vera migrate", it does not require a full application config.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	fmt.Println("migrations applied successfully")
}
