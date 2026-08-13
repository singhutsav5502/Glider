package vendors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glider-ai/glider/internal/atomicfile"
)

// GrantResumePermission implements the part of agyAdapter for the one
// behaviour at execution time that the uniform CommandTemplate model cannot
// hold.
//
// History. Refer to planning/permission_relay_design.md §2.3, §7 and §8.
//
// A write to settings.json was the first idea. A person refused it on
// 2026-07-26, because it changes too much and it is too specific to agy for
// the core design. The alternative was a resume template with
// --dangerously-skip-permissions or --continue. A person implemented that
// alternative. Then live tests against the true agy.exe showed that it does
// not operate:
//
//   - --continue and --conversation<id> both resumed a different conversation,
//     the "most recent" one, and not the conversation with the refusal. A
//     person reproduced this more than 4 times, and one time immediately after
//     the refusal.
//   - --dangerously-skip-permissions made the model explain the flag, and the
//     model did not do the task. A person reproduced this with 4 different
//     positions of the flag and 4 different forms of the prompt.
//
// Then the changelog of the CLI of agy answered the question. Version 1.1.3
// says that "the CLI now soft-denies such tools and prints a stderr notice
// naming the allow-rule needed". Version 1.1.6 says that "headless (-p)
// runs... now honor persisted settings.json policies, including permissions".
// Therefore permissions.allow in settings.json is the mechanism that agy
// DESIGNED for a permission with no console. It is not a method that goes
// around the design.
//
// A person approved this method again on 2026-07-26, with knowledge of those
// facts. A live test then confirmed it: an allow rule stops the hard
// "command" refusal of agy. The next attempt gives no error on stderr. The
// test made a copy of the true settings.json file and put it back after.
//
// Extended on 2026-07-26, the same day, to also write the permission in a
// PROJECT config for one directory, when such a config exists. The changelog
// of agy confirms that "project-specific configurations (located in
// ~/.gemini/config/projects/) take precedence over global settings in
// ~/.gemini/antigravity-cli/settings.json". A person can open a workspace one
// time in the interactive mode. That workspace then has its own project file.
// Therefore it would ignore a permission that is in the global file only, and
// it would give no message.
//
// A person found the true schema live, by a read of the true project files on
// this machine. Each one is a JSON file at
// ~/.gemini/config/projects/<id>.json. It identifies the directory that it
// belongs to at projectResources.resources[].gitFolder.folderUri, which is a
// file:// URI. It has its own allow list at the confirmed true path
// permissionGrants.permissionGrants.allow. That path has two levels with the
// same name, which is strange but true. It uses the same "tool(target)" text
// as the global file.
//
// The limits of this function, stated directly:
//
//   - It permits the TOOL CATEGORY that agy refused, for example "command(*)"
//     for each refused "command" permission. It does not permit the one exact
//     command text. agy gives no rule syntax with a smaller scope. This is
//     confirmed: the error text of the CLI names exactly this "command
//     (<target>)" form, and --help shows no form with a smaller scope.
//   - The true project files on this machine always had rules with one exact
//     path, for example "write_file(C:\...\x.csv)". They had no "*" rule.
//     Therefore no person confirmed that the rule matcher of agy accepts "*"
//     in a project file, and a live test confirmed it only for the global
//     file. This code uses the syntax that is proven for the global file, and
//     it does not invent a different syntax with no proof.
//   - The revert puts back a copy of each file that this code changed, byte
//     for byte. It does not remove only the rules that it added. That method
//     is more simple, and it is safe if Glider damages the file. The cost: if
//     agy itself writes to the file during the resume call, for example to
//     change trustedWorkspaces, the revert loses that write.
//
// To make the model truly DO the action that the person asked for, and not
// ask a question, is a separate problem. Refer to WrapResumePrompt.
func (agyAdapter) GrantResumePermission(v Vendor, cwd string, denials []Denial) (func() error, error) {
	toolNames := uniqueDenialToolNames(denials)
	if len(toolNames) == 0 {
		return func() error { return nil }, nil
	}
	rules := make([]string, len(toolNames))
	for i, name := range toolNames {
		rules[i] = name + "(*)"
	}

	globalPath, err := agySettingsPath()
	if err != nil {
		return nil, fmt.Errorf("vendors: resolving agy settings.json path: %w", err)
	}
	globalRevert, err := grantRulesInFile(globalPath, []string{"permissions", "allow"}, rules)
	if err != nil {
		return nil, fmt.Errorf("vendors: granting in global agy settings.json: %w", err)
	}

	var projectRevert func() error
	if cwd != "" {
		projectPath, found, err := findProjectFileForDir(cwd)
		if err != nil {
			// This operation can fail, and that is acceptable. A failure of the scan must
			// not stop the global permission that already succeeded above. The causes of
			// such a failure are a directory that the code cannot read, or one file beside
			// the correct one that is damaged. The global file is still a true
			// alternative that operates, even if the search for the project file is
			// worse.
			projectRevert = func() error { return nil }
		} else if found {
			projectRevert, err = grantRulesInFile(projectPath, []string{"permissionGrants", "permissionGrants", "allow"}, rules)
			if err != nil {
				_ = globalRevert()
				return nil, fmt.Errorf("vendors: granting in agy project file %s: %w", projectPath, err)
			}
		} else {
			projectRevert = func() error { return nil }
		}
	} else {
		projectRevert = func() error { return nil }
	}

	return func() error {
		err1 := globalRevert()
		err2 := projectRevert()
		if err1 != nil {
			return err1
		}
		return err2
	}, nil
}

// WrapResumePrompt answers a true gap that a person confirmed separately.
// Refer to planning/ngl_and_adapters.md §9.
//
// The resume of agy always removes the permission gate, and the call after the
// resume gets no refusal. But the model frequently answers with a description
// of the workspace, and it does not do the action that the person asked for. A
// person reproduced this 6 times in sequence, live, with different forms of
// the prompt. None of those forms had this specific text about "you already
// have permission" at the moment of the resume.
//
// This is a mitigation with the prompt, and it is not a correction that always
// operates. This comment says that directly, and it does not say more than the
// truth. The model can still avoid the work.
//
// The true and complete correction, for one attempt that always operates, is
// Path B, which is the interactive mode. This function does not try to replace
// Path B.
func (agyAdapter) WrapResumePrompt(prompt string) string {
	return "Permission for this action has already been granted. Do not describe the directory or ask a follow-up question — perform the action directly: " + prompt
}

// ExtraResumeArgs is nil for agy. Its permission for each refusal is a true
// change to settings.json, in GrantResumePermission above. It is not a flag in
// the argv of the resume. The resume of agy is a plain --continue, and it has
// no flag for one tool to add to.
func (agyAdapter) ExtraResumeArgs(denials []Denial) []string { return nil }

func uniqueDenialToolNames(denials []Denial) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range denials {
		if d.ToolName == "" || seen[d.ToolName] {
			continue
		}
		seen[d.ToolName] = true
		out = append(out, d.ToolName)
	}
	return out
}

func containsAnyString(list []any, want string) bool {
	for _, v := range list {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

// grantRulesInFile does four steps.
//
//  1. It reads path, which is a JSON object. If the file does not exist, it
//     uses {} as the content.
//  2. It makes sure that each rule in rules is in the array at the nested key
//     path. It makes the objects between, and the array, when they are absent.
//  3. It writes the file again, if any value changed.
//  4. It returns a function that reverts the change. That function puts the
//     exact original bytes back. If this call made the file, that function
//     removes the file.
//
// If each rule was already present, this function changes nothing, and the
// revert function does nothing.
func grantRulesInFile(path string, keyPath []string, rules []string) (func() error, error) {
	original, existed, err := readIfExists(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	doc := map[string]any{}
	if existed {
		if err := json.Unmarshal(original, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON, refusing to modify it: %w", path, err)
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	cur := doc
	for _, key := range keyPath[:len(keyPath)-1] {
		next, _ := cur[key].(map[string]any)
		if next == nil {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
	lastKey := keyPath[len(keyPath)-1]
	allow, _ := cur[lastKey].([]any)
	changed := false
	for _, rule := range rules {
		if !containsAnyString(allow, rule) {
			allow = append(allow, rule)
			changed = true
		}
	}
	if !changed {
		return func() error { return nil }, nil
	}
	cur[lastKey] = allow

	updated, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling updated %s: %w", path, err)
	}
	// Use atomicfile, and not os.WriteFile.
	//
	// This is the true external settings.json of the user for agy. Glider does not
	// own that file.
	//
	// A failure during a write here damages a config file, and Glider cannot help
	// the user to repair it. It does not only lose the state of Glider.
	//
	// This applies to this call, and also to the revert function below. That
	// function operates again for each delegate call that resumes.
	if err := atomicfile.WriteFile(path, updated, 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}

	return func() error {
		if !existed {
			return os.Remove(path)
		}
		return atomicfile.WriteFile(path, original, 0o644)
	}, nil
}

// readIfExists reads path, reporting existed=false (no error) for a
// missing file rather than making the caller special-case os.IsNotExist.
func readIfExists(path string) (data []byte, existed bool, err error) {
	data, err = os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// agySettingsPath gives the true GLOBAL settings file of agy, which is
// ~/.gemini/antigravity-cli/settings.json. A live test confirmed it on
// 2026-07-26.
//
// This is a DIFFERENT file from ~/.gemini/config/config.json. That second file
// also exists on a machine with agy, and it also holds data about permissions.
//
// Do not use one file in place of the other. A run with no console, from -p,
// uses antigravity-cli/settings.json only. The changelog above gives that
// fact.
func agySettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), nil
}

// agyProjectsDir resolves ~/.gemini/config/projects — where agy stores one
// JSON file per known project.
func agyProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "config", "projects"), nil
}

// findProjectFileForDir searches the projects that agy knows, for one project
// with a directory that agrees with dir.
//
// A result of ok=false with a nil error is the usual condition. It occurs for a
// target of one delegation, which no person opened in the interactive mode. That
// is not a failure. It only means "there is nothing to add the permission to,
// other than the global file".
//
// The true project files on this machine use TWO shapes for the same field, and
// not one:
//
//   - A project with a git repository puts it under
//     resources[].gitFolder.folderUri.
//   - A project with a plain directory puts it directly at
//     resources[].folderUri. Such a project has no git repository. An example is
//     a temporary directory, or a directory that a person connected with a plain
//     "folderUri" that some versions of agy write.
//
// An earlier version of this function examined only the form with gitFolder.
//
// A live test on 2026-07-26 confirmed the result, while a person built the
// writer for the permission presets. That version did not see each file with the
// plain form, and it gave no message.
//
// Therefore GrantResumePermission did not add its permission for those
// directories. It used the global permission only. No person saw this, because
// the global permission still succeeded.
//
// The correction examines both shapes.
func findProjectFileForDir(dir string) (path string, ok bool, err error) {
	projectsDir, err := agyProjectsDir()
	if err != nil {
		return "", false, err
	}
	entries, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	wantURI, err := dirToFileURI(dir)
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		p := filepath.Join(projectsDir, entry.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue // one unreadable sibling shouldn't abort the whole scan
		}
		var doc struct {
			ProjectResources struct {
				Resources []struct {
					FolderURI string `json:"folderUri"` // bare form
					GitFolder struct {
						FolderURI string `json:"folderUri"` // git-repo-bound form
					} `json:"gitFolder"`
				} `json:"resources"`
			} `json:"projectResources"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			continue
		}
		for _, r := range doc.ProjectResources.Resources {
			if strings.EqualFold(r.FolderURI, wantURI) || strings.EqualFold(r.GitFolder.FolderURI, wantURI) {
				return p, true, nil
			}
		}
	}
	return "", false, nil
}

// dirToFileURI converts an absolute filesystem path into the file:// URI
// form confirmed live in real project files (e.g.
// "file:///C:/Users/Utsav/Documents/antigravity/keen-einstein" — forward
// slashes, uppercase drive letter, no trailing slash).
func dirToFileURI(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs = filepath.ToSlash(abs)
	if len(abs) >= 2 && abs[1] == ':' {
		return "file:///" + strings.ToUpper(abs[:1]) + abs[1:], nil
	}
	return "file://" + abs, nil
}
