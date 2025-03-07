package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/laizy/web3/abigen"
	"github.com/laizy/web3/compiler"
)

const (
	version = "0.1.0"
)

func main() {
	var source string
	var pckg string
	var output string
	var name string

	flag.StringVar(&source, "source", "", "List of abi files")
	flag.StringVar(&pckg, "package", "main", "Name of the package")
	flag.StringVar(&output, "output", "", "Output directory")
	flag.StringVar(&name, "name", "", "name of the contract")

	flag.Parse()

	config := &abigen.Config{
		Package: pckg,
		Output:  output,
		Name:    name,
	}

	if source == "" {
		fmt.Println(version)
		os.Exit(0)
	}

	matches, err := filepath.Glob(source)
	if err != nil {
		fmt.Printf("Failed to read files: %v", err)
		os.Exit(1)
	}
	for _, source := range matches {
		artifacts, err := process(source, config)
		if err != nil {
			fmt.Printf("Failed to parse sources: %v", err)
			os.Exit(1)
		}
		if err := abigen.GenCode(artifacts, config); err != nil {
			fmt.Printf("Failed to generate sources: %v", err)
			os.Exit(1)
		}
	}
}

const (
	vyExt   = 0
	solExt  = 1
	abiExt  = 2
	jsonExt = 3
)

func process(sources string, config *abigen.Config) (map[string]*compiler.Artifact, error) {
	files := strings.Split(sources, ",")
	if len(files) == 0 {
		return nil, fmt.Errorf("input not found")
	}

	prev := -1
	for _, f := range files {
		var ext int
		switch extt := filepath.Ext(f); extt {
		case ".abi":
			ext = abiExt
		case ".sol":
			ext = solExt
		case ".vy", ".py":
			ext = vyExt
		case ".json":
			ext = jsonExt
		default:
			return nil, fmt.Errorf("file extension '%s' not found", extt)
		}

		if prev == -1 {
			prev = ext
		} else if ext != prev {
			return nil, fmt.Errorf("two file formats found")
		}
	}

	switch prev {
	case abiExt:
		return processAbi(files, config)
	case solExt:
		return processSolc(files)
	case vyExt:
		return processVyper(files)
	case jsonExt:
		return processJson(files)
	}

	return nil, nil
}

func processVyper(sources []string) (map[string]*compiler.Artifact, error) {
	c, err := compiler.NewCompiler("vyper", "vyper")
	if err != nil {
		return nil, err
	}
	raw, err := c.Compile(sources...)
	if err != nil {
		return nil, err
	}
	res := map[string]*compiler.Artifact{}
	for rawName, entry := range raw {
		_, name := filepath.Split(rawName)
		name = strings.TrimSuffix(name, ".vy")
		name = strings.TrimSuffix(name, ".v.py")
		res[strings.Title(name)] = entry
	}
	return res, nil
}

func processSolc(sources []string) (map[string]*compiler.Artifact, error) {
	c, err := compiler.NewCompiler("solidity", "solc")
	if err != nil {
		return nil, err
	}
	raw, err := c.Compile(sources...)
	if err != nil {
		return nil, err
	}
	res := map[string]*compiler.Artifact{}
	for rawName, entry := range raw {
		name := strings.Split(rawName, ":")[1]
		res[strings.Title(name)] = entry
	}
	return res, nil
}

func processAbi(sources []string, config *abigen.Config) (map[string]*compiler.Artifact, error) {
	artifacts := map[string]*compiler.Artifact{}

	for _, abiPath := range sources {
		content, err := ioutil.ReadFile(abiPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read abi file (%s): %v", abiPath, err)
		}

		// Use the name of the file to name the contract
		path, name := filepath.Split(abiPath)

		name = strings.TrimSuffix(name, filepath.Ext(name))
		binPath := filepath.Join(path, name+".bin")

		bin, err := ioutil.ReadFile(binPath)
		if err != nil {
			// bin not found
			bin = []byte{}
		}
		if len(sources) == 1 && config.Name != "" {
			name = config.Name
		}
		artifacts[strings.Title(name)] = &compiler.Artifact{
			Abi: string(content),
			Bin: string(bin),
		}
	}
	return artifacts, nil
}

type JSONArtifact struct {
	Bytecode     string          `json:"bytecode"`
	Abi          json.RawMessage `json:"abi"`
	DeployedCode string          `json:"deployedBytecode"`
}

func processJson(sources []string) (map[string]*compiler.Artifact, error) {
	artifacts := map[string]*compiler.Artifact{}

	for _, jsonPath := range sources {
		content, err := ioutil.ReadFile(jsonPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read abi file (%s): %v", jsonPath, err)
		}

		// Use the name of the file to name the contract
		_, name := filepath.Split(jsonPath)
		name = strings.TrimSuffix(name, ".json")

		var art *JSONArtifact
		if err := json.Unmarshal(content, &art); err != nil {
			return nil, err
		}

		artifacts[strings.Title(name)] = compiler.NewArtifact(string(art.Abi), art.Bytecode, art.DeployedCode)
	}
	return artifacts, nil
}
