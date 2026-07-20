package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		_, _ = os.Stdout.WriteString(version + "\n")
		return
	}
	if err := os.WriteFile("/data/demo-version", []byte(version+"\n"), 0o644); err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/api/version", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode(map[string]string{"version": version}); err != nil {
			log.Printf("write version response: %v", err)
		}
	})
	log.Printf("installer Docker demo %s listening on :7410", version)
	log.Fatal(http.ListenAndServe(":7410", nil))
}
