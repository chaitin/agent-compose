package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/google/go-containerregistry/pkg/registry"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:0", "HTTP listen address")
	root := flag.String("root", "", "release asset directory")
	addressFile := flag.String("address-file", "", "file receiving the selected address")
	pidFile := flag.String("pid-file", "", "file receiving the server process ID")
	flag.Parse()
	if *root == "" || *addressFile == "" {
		log.Fatal("--root and --address-file are required")
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*addressFile, []byte(listener.Addr().String()+"\n"), 0o600); err != nil {
		_ = listener.Close()
		log.Fatal(err)
	}
	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
			_ = listener.Close()
			log.Fatal(err)
		}
	}
	registryHandler := registry.New(registry.Logger(log.New(os.Stderr, "registry: ", log.LstdFlags)))
	releaseHandler := http.FileServer(http.Dir(*root))
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v2/" || len(request.URL.Path) > len("/v2/") && request.URL.Path[:len("/v2/")] == "/v2/" {
			registryHandler.ServeHTTP(response, request)
			return
		}
		releaseHandler.ServeHTTP(response, request)
	})
	log.Printf("demo release and registry server listening at http://%s", listener.Addr())
	if err := http.Serve(listener, handler); err != nil {
		log.Fatal(fmt.Errorf("serve demo endpoints: %w", err))
	}
}
