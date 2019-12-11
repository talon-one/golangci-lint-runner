package golangci_lint_runner

import (
	"fmt"
	"io/ioutil"
	"testing"

	"log"

	"os"

	"path/filepath"

	"github.com/golangci/golangci-lint/pkg/config"
	jsoniter "github.com/json-iterator/go"
	"github.com/stretchr/testify/require"
)

func TestRunner_readRepoConfig(t *testing.T) {
	defaultConfig := config.Config{
		Run: config.Run{
			Config: ".golangci.yml",
		},
		LintersSettings: config.LintersSettings{
			Unparam: config.UnparamSettings{
				Algo: "cha",
			},
		},
		Linters: config.Linters{
			Enable: []string{"Hello", "World"},
		},
	}

	type fields struct {
		meta    MetaData
		Options *Options
	}
	tests := []struct {
		name         string
		fields       fields
		wantErr      bool
		repoConfig   *config.Config
		expectConfig config.Config
	}{
		{
			name: "no config file present",
			fields: struct {
				meta    MetaData
				Options *Options
			}{
				meta: MetaData{},
				Options: &Options{
					Logger:       logger{},
					LinterConfig: defaultConfig,
				},
			},
			wantErr:      false,
			repoConfig:   nil,
			expectConfig: defaultConfig,
		},

		{
			name: "config file present",
			fields: struct {
				meta    MetaData
				Options *Options
			}{
				meta: MetaData{},
				Options: &Options{
					Logger:       logger{},
					LinterConfig: defaultConfig,
				},
			},
			repoConfig: &config.Config{
				Linters: config.Linters{
					Enable: []string{"Bye"},
				},
			},
			wantErr: false,
			expectConfig: config.Config{
				Run: config.Run{
					Config: ".golangci.yml",
				},
				LintersSettings: config.LintersSettings{
					Unparam: config.UnparamSettings{
						Algo: "cha",
					},
				},
				Linters: config.Linters{
					Enable: []string{"Bye"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := ioutil.TempDir("", "golangci-lint-runner-test-")
			if err != nil {
				t.Fatal()
				return
			}
			defer os.RemoveAll(dir)
			r := &Runner{
				meta:    tt.fields.meta,
				Options: tt.fields.Options,
			}

			if tt.repoConfig != nil {
				file, err := os.Create(filepath.Join(dir, tt.fields.Options.LinterConfig.Run.Config))
				if err != nil {
					t.Fatal()
					return
				}
				var json = jsoniter.Config{
					EscapeHTML:             true,
					SortMapKeys:            true,
					ValidateJsonRawMessage: true,
					TagKey:                 "mapstructure",
				}.Froze()
				err = json.NewEncoder(file).Encode(tt.repoConfig)
				if err != nil {
					file.Close()
					t.Fatal()
					return
				}
				file.Close()
			}

			if err := r.readRepoConfig(dir); (err != nil) != tt.wantErr {
				t.Errorf("readRepoConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			require.Equal(t, tt.expectConfig, r.Options.LinterConfig)
		})
	}
}

type logger struct{}

func (logger) Debug(format string, a ...interface{}) {
	log.Println(fmt.Sprintf("[DEBUG] "+format, a...))
}

func (logger) Info(format string, a ...interface{}) {
	log.Println(fmt.Sprintf(format, a...))
}

func (logger) Warn(format string, a ...interface{}) {
	log.Println(fmt.Sprintf("Warning: "+format, a...))
}

func (logger) Error(format string, a ...interface{}) {
	log.Println(fmt.Sprintf("Error: "+format, a...))
}
