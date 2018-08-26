package validator

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/garethr/kubeval/kubeval"
	"github.com/google/go-github/github"
	yamlpatch "github.com/krishicks/yaml-patch"
	difflib "github.com/pmezard/go-difflib/difflib"
	"github.com/xeipuuv/gojsonschema"
	"sourcegraph.com/sourcegraph/go-diff/diff"
)

// Candidate reprensets a file to be validated
type Candidate struct {
	bytes   *[]byte
	context *Context
	file    *github.CommitFile
	schemas []*KubeValidatorConfigSchema
}

var (
	defaultSchema = &KubeValidatorConfigSchema{
		Version:    "master",
		SchemaFork: "garethr",
		ConfigType: "kubernetes",
		Strict:     false,
	}
)

// NewCandidate initializes a validation Candidate
func NewCandidate(context *Context, file *github.CommitFile, schemas []*KubeValidatorConfigSchema) *Candidate {
	if len(schemas) == 0 {
		schemas = append(schemas, defaultSchema)
	}
	return &Candidate{
		context: context,
		file:    file,
		schemas: schemas,
	}
}

func (c *Candidate) setBytes(bytes *[]byte) {
	c.bytes = bytes
}

// LoadBytes hydrates bytes from GitHub and returns a CheckRunAnnotation if
// an error is encountered
func (c *Candidate) LoadBytes() *github.CheckRunAnnotation {
	bytes, err := c.context.bytesForFilename(c.context.Event.(*github.CheckSuiteEvent), c.file.GetFilename())
	if err != nil {
		return &github.CheckRunAnnotation{
			FileName:     c.file.Filename,
			BlobHRef:     c.file.BlobURL,
			StartLine:    github.Int(1),
			EndLine:      github.Int(1),
			WarningLevel: github.String("failure"),
			Title:        github.String(fmt.Sprintf("Error loading %s", c.file.GetFilename())),
			Message:      github.String(fmt.Sprintf("%+v", err)),
		}
	}

	c.bytes = bytes
	return nil
}

// MarkdownListItem returns a string that represents the Candidate designed for
// use in a Markdown List
func (c *Candidate) MarkdownListItem() string {
	return fmt.Sprintf("* [`./%s`](%s)", c.file.GetFilename(), c.file.GetBlobURL())
}

// Validate bytes with kubeval and return an array of CheckRunAnnotation
func (c *Candidate) Validate() []*github.CheckRunAnnotation {
	var annotations []*github.CheckRunAnnotation
	for _, schema := range c.schemas {
		kubeval.SchemaLocation = schema.SchemaLocation()

		// TODO move more of this into KubeValidatorConfigSchema
		if schema.Version != "" {
			kubeval.Version = schema.Version
		}

		kubeval.Strict = schema.Strict
		if schema.ConfigType == "openstack" {
			kubeval.OpenShift = true
		} else {
			kubeval.OpenShift = false
		}

		var schemaName string
		if schema.Name != "" {
			schemaName = schema.Name
		} else if schema.Version != "" {
			schemaName = schema.Version
		} else {
			schemaName = fmt.Sprintf("%v", schema)
		}

		results, err := kubeval.Validate(*c.bytes, c.file.GetFilename())

		if err != nil {
			annotations = append(annotations, &github.CheckRunAnnotation{
				FileName:     c.file.Filename,
				BlobHRef:     c.file.BlobURL,
				StartLine:    github.Int(1),
				EndLine:      github.Int(1),
				WarningLevel: github.String("failure"),
				Title:        github.String(fmt.Sprintf("Error validating %s against %s schema", results[0].Kind, schemaName)),
				Message:      github.String(fmt.Sprintf("%+v", err)),
			})
			continue
		}

		for _, result := range results {
			for _, error := range result.Errors {
				start, end := lineNumbers(c.bytes, error)
				annotations = append(annotations, &github.CheckRunAnnotation{
					FileName:     c.file.Filename,
					BlobHRef:     c.file.BlobURL,
					StartLine:    &start,
					EndLine:      &end,
					WarningLevel: github.String("failure"),
					Title:        github.String(fmt.Sprintf("Error validating %s against %s schema", results[0].Kind, schemaName)),
					Message:      github.String(error.String()),
					RawDetails:   github.String(resultErrorDetailString(error)),
				})
			}
		}
	}
	return annotations
}

func lineNumbers(bytes *[]byte, e gojsonschema.ResultError) (int, int) {
	dotted := strings.TrimPrefix(e.Context().String(), "(root)")
	path := yamlpatch.OpPath(strings.Replace(dotted, ".", "/", -1))
	var patch yamlpatch.Patch
	operation := yamlpatch.Operation{
		Op:   "remove",
		Path: path,
	}
	patch = append(patch, operation)

	patchedBytes, err := patch.Apply(*bytes)
	if err != nil {
		return 1, 1
	}

	difflibDiff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(*bytes)),
		B:        difflib.SplitLines(string(patchedBytes)),
		FromFile: "Original",
		ToFile:   "Current",
		Context:  3,
	}
	unifiedDiffString, _ := difflib.GetUnifiedDiffString(difflibDiff)
	fileDiff, _ := diff.ParseFileDiff([]byte(unifiedDiffString))

	startLine := int(fileDiff.Hunks[0].OrigStartLine)
	endLine := int(fileDiff.Hunks[0].OrigStartLine + fileDiff.Hunks[0].OrigLines)

	return startLine, endLine
}

func resultErrorDetailString(e gojsonschema.ResultError) string {
	details := e.Details()
	var buffer bytes.Buffer
	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		buffer.WriteString(fmt.Sprintf("* %s: %s\n", k, details[k]))
	}

	return buffer.String()
}
