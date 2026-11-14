
package main

import (
    "fmt"
    "log"
    "net/http"
)

func main() {
    mux := http.NewServeMux()

    mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Hello from Backend!\n")
        fmt.Fprintf(w, "Method: %s\n", r.Method)
        fmt.Fprintf(w, "Path: %s\n", r.URL.Path)
        fmt.Fprintf(w, "Query: %s\n", r.URL.RawQuery)
    })

    log.Println("Backend listening on :9090")

    log.Fatal(http.ListenAndServe(":9090", mux))
}
