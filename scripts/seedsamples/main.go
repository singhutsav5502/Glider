// Command seedsamples loads all sample hoop and swarm YAML into Glider's runtime store.
//
//	go run ./scripts/seedsamples
//	go run ./scripts/seedsamples -start
//	go run ./scripts/seedsamples -base http://127.0.0.1:8081
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
	"sort"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/loop"
	"github.com/glider-ai/glider/internal/swarm"
	"gopkg.in/yaml.v3"
)

type kindProbe struct {
	Kind string `yaml:"kind"`
	ID   string `yaml:"id"`
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8081", "dashboard base URL")
	samplesRoot := flag.String("samples", "", "repo samples root (default: <repo>/samples)")
	hoopsDir := flag.String("hoops-dir", "", "swarm template dir (default: ~/.glider/hoops)")
	start := flag.Bool("start", false, "POST .../start after seeding each hoop")
	timeout := flag.Duration("timeout", 30*time.Second, "HTTP timeout")
	flag.Parse()

	root, err := resolveSamplesRoot(*samplesRoot)
	if err != nil {
		fatal(err)
	}
	hoopSamples := filepath.Join(root, "hoops")
	swarmSamples := filepath.Join(root, "swarms")

	paths, err := discoverYAML(hoopSamples, swarmSamples)
	if err != nil {
		fatal(err)
	}
	if len(paths) == 0 {
		fatal(fmt.Errorf("no sample YAML found under %s", root))
	}
	sort.Strings(paths)

	store := swarm.NewTemplateStore(*hoopsDir)
	client := &http.Client{Timeout: *timeout}

	var (
		hoopsOK, swarmsOK int
		failures          int
	)

	fmt.Printf("seeding from %s → dashboard %s, templates %s (%d files)\n", root, strings.TrimRight(*base, "/"), store.Dir, len(paths))

	for _, p := range paths {
		kind, id, err := probeKind(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fail %s: %v\n", p, err)
			failures++
			continue
		}
		switch kind {
		case "hoop", "":
			spec, err := loop.ReadHoopYAMLFile(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "fail %s: %v\n", p, err)
				failures++
				continue
			}
			if err := upsertHoop(client, *base, spec); err != nil {
				fmt.Fprintf(os.Stderr, "fail hoop %s (%s): %v\n", spec.ID, p, err)
				failures++
				continue
			}
			fmt.Printf("hoop   %s ← %s\n", spec.ID, rel(root, p))
			hoopsOK++
			if *start {
				if err := post(client, strings.TrimRight(*base, "/")+"/api/loops/"+spec.ID+"/start", nil); err != nil {
					fmt.Fprintf(os.Stderr, "fail start %s: %v\n", spec.ID, err)
					failures++
					continue
				}
				fmt.Printf("start  %s\n", spec.ID)
			}
		case "swarm_template", "swarm":
			tpl, err := readSwarmTemplate(p, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "fail %s: %v\n", p, err)
				failures++
				continue
			}
			if err := store.Save(tpl); err != nil {
				fmt.Fprintf(os.Stderr, "fail swarm %s (%s): %v\n", tpl.ID, p, err)
				failures++
				continue
			}
			fmt.Printf("swarm  %s ← %s\n", tpl.ID, rel(root, p))
			swarmsOK++
		default:
			fmt.Fprintf(os.Stderr, "skip %s: unsupported kind %q\n", p, kind)
		}
	}

	fmt.Printf("done  hoops=%d swarms=%d failures=%d\n", hoopsOK, swarmsOK, failures)
	if failures > 0 {
		os.Exit(1)
	}
}

func resolveSamplesRoot(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(explicit)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidates := []string{
		filepath.Join(wd, "samples"),
		filepath.Join(wd, "..", "samples"),
		filepath.Join(wd, "..", "..", "samples"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if st, err := os.Stat(filepath.Join(abs, "hoops")); err == nil && st.IsDir() {
			return abs, nil
		}
	}
	return "", fmt.Errorf("samples/ not found from %s (pass -samples)", wd)
}

func discoverYAML(dirs ...string) ([]string, error) {
	var out []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
				continue
			}
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out, nil
}

func probeKind(path string) (kind, id string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var p kindProbe
	if err := yaml.Unmarshal(data, &p); err != nil {
		return "", "", fmt.Errorf("yaml: %w", err)
	}
	kind = strings.ToLower(strings.TrimSpace(p.Kind))
	id = strings.TrimSpace(p.ID)
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return kind, id, nil
}

func readSwarmTemplate(path, fallbackID string) (*swarm.Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var nested struct {
		Kind     string         `yaml:"kind"`
		Template swarm.Template `yaml:"template"`
	}
	if err := yaml.Unmarshal(data, &nested); err == nil && (nested.Template.ID != "" || nested.Template.Prompt != "") {
		t := nested.Template
		if t.ID == "" {
			t.ID = fallbackID
		}
		t.Enabled = true
		if err := t.Normalize(); err != nil {
			return nil, err
		}
		return &t, nil
	}
	var flat struct {
		Kind           string `yaml:"kind"`
		swarm.Template `yaml:",inline"`
	}
	if err := yaml.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	t := flat.Template
	if t.ID == "" {
		t.ID = fallbackID
	}
	t.Enabled = true
	if err := t.Normalize(); err != nil {
		return nil, err
	}
	return &t, nil
}

func upsertHoop(client *http.Client, base string, spec loop.LoopSpec) error {
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	url := strings.TrimRight(base, "/") + "/api/loops"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
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

func rel(root, path string) string {
	r, err := filepath.Rel(filepath.Dir(root), path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
