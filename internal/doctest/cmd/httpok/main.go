// Copyright 2026 HAProxy Technologies LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// httpok is a doc-test stand-in web service: answers every request with 200.
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	addr := "127.0.0.1:8080"
	if len(os.Args) >= 3 && os.Args[1] == "--listen" {
		addr = os.Args[2]
	}
	err := http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	log.Fatal(err)
}
