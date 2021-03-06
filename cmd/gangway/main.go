// Copyright © 2017 Heptio
// Copyright © 2017 Craig Tracey
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/sessions"
	"github.com/justinas/alice"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
)

var cfg *Config
var oauth2Cfg *oauth2.Config
var sessionStore *sessions.CookieStore
var httpClient *http.Client

// wrapper function for http logging
func httpLogger(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer log.Printf("%s %s %s", r.Method, r.URL, r.RemoteAddr)
		fn(w, r)
	}
}

func main() {

	cfgFile := flag.String("config", "", "The config file to use.")
	flag.Parse()

	var err error
	cfg, err = NewConfig(*cfgFile)
	if err != nil {
		log.Errorf("Could not parse config file: %s", err)
		os.Exit(1)
	}

	oauth2Cfg = &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  cfg.AuthorizeURL,
			TokenURL: cfg.TokenURL,
		},
	}

	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}

	if cfg.TrustedCAPath != "" {
		// Read in the cert file
		certs, err := ioutil.ReadFile(cfg.TrustedCAPath)
		if err != nil {
			log.Fatalf("Failed to append %q to RootCAs: %v", cfg.TrustedCAPath, err)
		}

		// Append our cert to the system pool
		if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
			log.Println("No certs appended, using system certs only")
		}
	}

	// Trust the augmented cert pool in our client
	config := &tls.Config{
		RootCAs: rootCAs,
	}
	tr := &http.Transport{TLSClientConfig: config}
	httpClient = &http.Client{Transport: tr}

	initSessionStore()

	loginRequiredHandlers := alice.New(loginRequired)

	http.HandleFunc("/", httpLogger(homeHandler))
	http.HandleFunc("/login", httpLogger(loginHandler))
	http.HandleFunc("/callback", httpLogger(callbackHandler))

	// middleware'd routes
	http.Handle("/logout", loginRequiredHandlers.ThenFunc(logoutHandler))
	http.Handle("/commandline", loginRequiredHandlers.ThenFunc(commandlineHandler))

	bindAddr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	// create http server with timeouts
	httpServer := &http.Server{
		Addr:         bindAddr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// start up the http server
	go func() {
		// exit with FATAL logging why we could not start
		// example: FATA[0000] listen tcp 0.0.0.0:8080: bind: address already in use
		if cfg.ServeTLS == true {
			log.Fatal(httpServer.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile))
		} else {
			log.Fatal(httpServer.ListenAndServe())
		}
	}()

	// create channel listening for signals so we can have graceful shutdowns
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	log.Println("Shutdown signal received, exiting.")
	// close the HTTP server
	httpServer.Shutdown(context.Background())

}
