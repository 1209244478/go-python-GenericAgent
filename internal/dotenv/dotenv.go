package dotenv

import (
	"bufio"
	"os"
	"strings"
)

func Load(path string) map[string]string {
	env := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return env
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 1 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		env[key] = val
	}
	return env
}

func FindAndLoad(rootDir string) map[string]string {
	candidates := []string{
		".env",
	}
	for _, c := range candidates {
		p := c
		if !strings.HasPrefix(p, "/") {
			p = rootDir + "/" + c
		}
		if env := Load(p); len(env) > 0 {
			return env
		}
	}
	return make(map[string]string)
}

func Get(env map[string]string, key string, def string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	return def
}
