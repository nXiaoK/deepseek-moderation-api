// setup writes local configuration without printing credentials. Existing files
// are never overwritten. Load the resulting .env before starting the server.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func main() {
	f, err := os.OpenFile(".env", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot create .env (existing files are preserved):", err)
		os.Exit(1)
	}
	defer f.Close()
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		panic(err)
	}
	_, err = fmt.Fprintf(f, "MASTER_KEY=%s\nADMIN_USER=admin\nADMIN_PASSWORD=%s\nPOSTGRES_PASSWORD=%s\nDATABASE_URL=postgres://audit@127.0.0.1:55439/audit?sslmode=disable\nPUBLIC_URL=http://localhost:8090\nLISTEN_ADDR=127.0.0.1:8090\nSTATIC_DIR=frontend/dist\n", base64.StdEncoding.EncodeToString(key), token(24), token(24))
	if err != nil {
		panic(err)
	}
	fmt.Println("Created .env with owner-only permissions. Read ADMIN_PASSWORD there to sign in.")
}
