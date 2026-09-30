// set-password sets the owner's login password (bcrypt hash in the users table).
//
//	set-password -generate            # create a random password and print it once
//	echo 'my-secret' | set-password   # read the password from stdin
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/config"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/db"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

// No look-alike characters (0/O, 1/l/I) so the password is easy to type.
const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func generate(n int) string {
	var b strings.Builder
	for range n {
		i, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		b.WriteByte(alphabet[i.Int64()])
	}
	return b.String()
}

func main() {
	cfg := config.Load()
	gen := flag.Bool("generate", false, "generate a random 16-character password and print it")
	email := flag.String("email", cfg.OwnerEmail, "account email (must be RAKUMA_OWNER_EMAIL)")
	flag.Parse()

	var pw string
	if *gen {
		pw = generate(16)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "đọc mật khẩu từ stdin, hoặc dùng -generate")
			os.Exit(2)
		}
		pw = strings.TrimRight(line, "\r\n")
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	if err := service.New(pool, cfg).SetPassword(ctx, *email, pw); err != nil {
		fmt.Fprintln(os.Stderr, "set-password:", err)
		os.Exit(1)
	}
	fmt.Printf("Đã đặt mật khẩu cho %s\n", strings.ToLower(*email))
	if *gen {
		fmt.Printf("Mật khẩu: %s\n", pw)
	}
}
