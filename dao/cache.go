package dao

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	ini "github.com/timothyclarke/http-request-broadcaster/ini"
)

type Cache struct {
	Name    string      `json:"name"`
	Address string      `json:"address"`
	Method  string      `json:"-"`
	Item    string      `json:"-"`
	Headers http.Header `json:"-"`
}

type Group struct {
	Name   string  `json:"name"`
	Caches []Cache `json:"caches"`
}

func LoadCachesFromJson(configPath string) ([]Group, error) {
	var groups []Group

	_, err := os.Stat(configPath)
	if err != nil {
		return groups, err
	}

	fileContent, err := ioutil.ReadFile(configPath)
	if err != nil {
		return groups, err
	}

	err = json.Unmarshal(fileContent, &groups)

	return groups, err
}

func LoadCachesFromIni(configPath string) ([]Group, error) {
	var groups []Group
	cfg, err := ini.Load(configPath)

	if err != nil {
		return groups, err
	}

	for _, s := range cfg.Sections() {

		var g Group

		for _, k := range s.Keys() {
			var c Cache
			c.Name = k.Name()
			c.Address = k.Value()
			g.Caches = append(g.Caches, c)

		}
		g.Name = s.Name()
		groups = append(groups, g)
	}

	return groups, nil
}
