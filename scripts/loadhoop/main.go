// Command loadhoop creates (and optionally starts) Loop Engineering hoops from YAML.
//
//	go run ./scripts/loadhoop -file samples/hoops/hello-critic.yaml -start
//	go run ./scripts/loadhoop -dir samples/hoops
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/loop"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8081", "dashboard base URL")
	file := flag.String("file", "", "path to one hoop YAML")
	dir := flag.String("dir", "", "directory of hoop YAML files")
	start := flag.Bool("start", false, "POST .../start after create/update")
	idOnly := flag.String("id", "", "when using -dir -start, only start this id")
	timeout := flag.Duration("timeout", 30*time.Second, "HTTP timeout")
	flag.Parse()

	client := &http.Client{Timeout: *timeout}
	var paths []string
	switch {
	case *file != "":
		paths = []string{*file}
	case *dir != "":
		entries, err := os.ReadDir(*dir)
		if err != nil {
			fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
				continue
			}
			paths = append(paths, filepath.Join(*dir, name))
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: loadhoop -file path.yaml [-start] | -dir samples/hoops [-start] [-id hello-critic]")
		os.Exit(2)
	}

	for _, p := range paths {
		spec, err := loop.ReadHoopYAMLFile(p)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", p, err))
		}
		if err := upsert(client, *base, spec); err != nil {
			fatal(fmt.Errorf("%s: %w", spec.ID, err))
		}
		fmt.Printf("ok  %s (%s)\n", spec.ID, p)
		wantStart := *start && (*idOnly == "" || *idOnly == spec.ID)
		if wantStart {
			if err := post(client, *base+"/api/loops/"+spec.ID+"/start", nil); err != nil {
				fatal(fmt.Errorf("start %s: %w", spec.ID, err))
			}
			fmt.Printf("run %s started\n", spec.ID)
		}
	}
}

func upsert(client *http.Client, base string, spec loop.LoopSpec) error {
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	// Try create; on conflict update.
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/api/loops", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusCreated || res.StatusCode == http.StatusOK {
		return nil
	}
	// Already exists → PUT
	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusConflict {
		req2, err := http.NewRequest(http.MethodPut, strings.TrimRight(base, "/")+"/api/loops/"+spec.ID, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req2.Header.Set("Content-Type", "application/json")
		res2, err := client.Do(req2)
		if err != nil {
			return err
		}
		defer res2.Body.Close()
		raw2, _ := io.ReadAll(res2.Body)
		if res2.StatusCode >= 300 {
			return fmt.Errorf("PUT %s: %s", res2.Status, strings.TrimSpace(string(raw2)))
		}
		return nil
	}
	return fmt.Errorf("POST %s: %s", res.Status, strings.TrimSpace(string(raw)))
}

func post(client *http.Client, url string, body []byte) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
