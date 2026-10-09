//go:build schemasrefresh

// SPDX-FileCopyrightText: 2026 cordanaLLM contributors
//
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"strings"
	"testing"
)

func TestOpenRouterFragmentReflectsPinnedSchemaRename(t *testing.T) {
	original := syntheticOpenRouterDocument("context_length")
	renamed := syntheticOpenRouterDocument("context_window")

	gotOriginal, err := openRouterFragment(original)
	if err != nil {
		t.Fatalf("openRouterFragment(original) = %v", err)
	}
	gotRenamed, err := openRouterFragment(renamed)
	if err != nil {
		t.Fatalf("openRouterFragment(renamed) = %v", err)
	}
	if gotOriginal == gotRenamed {
		t.Fatal("openRouterFragment output did not change after Model.context_length was renamed")
	}
	if !strings.Contains(gotRenamed, "context_window:") {
		t.Fatalf("renamed fragment missing renamed field:\n%s", gotRenamed)
	}
}

func TestOllamaFragmentReflectsPinnedExampleRename(t *testing.T) {
	original := syntheticOllamaDocument("context_length")
	renamed := syntheticOllamaDocument("context_window")

	gotOriginal, err := ollamaFragment(original)
	if err != nil {
		t.Fatalf("ollamaFragment(original) = %v", err)
	}
	gotRenamed, err := ollamaFragment(renamed)
	if err != nil {
		t.Fatalf("ollamaFragment(renamed) = %v", err)
	}
	if gotOriginal == gotRenamed {
		t.Fatal("ollamaFragment output did not change after details.context_length was renamed")
	}
	if !strings.Contains(gotRenamed, `"context_window": 0`) {
		t.Fatalf("renamed fragment missing renamed field:\n%s", gotRenamed)
	}
	if strings.Contains(gotRenamed, "model-id") {
		t.Fatalf("derived fragment leaked concrete model name:\n%s", gotRenamed)
	}
}

func syntheticOpenRouterDocument(field string) string {
	return `openapi: 3.1.0
info:
  version: 1.0.0
paths:
  /models:
    get:
      operationId: getModels
      responses:
        '200':
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/ListModelsResponse'
  /other:
    get: {}
components:
  schemas:
    ListModelsResponse:
      type: object
      properties:
        data:
          type: array
          items:
            $ref: '#/components/schemas/Model'
    Model:
      type: object
      properties:
        id:
          type: string
        ` + field + `:
          type: integer
        pricing:
          $ref: '#/components/schemas/PublicPricing'
        architecture:
          $ref: '#/components/schemas/ModelArchitecture'
    ModelArchitecture:
      type: object
      properties:
        input_modalities:
          type: array
    PublicPricing:
      type: object
      properties:
        prompt:
          type: string
    Other:
      type: object
`
}

func syntheticOllamaDocument(field string) string {
	return `# API

## List Local Models

GET /api/tags

Response

` + "```json" + `
{
  "models": [
    {
      "name": "model-id",
      "details": {
        "` + field + `": 2048,
        "parameter_size": "1B",
        "quantization_level": "Q4"
      },
      "capabilities": ["completion"]
    }
  ]
}
` + "```" + `

## List Running Models

GET /api/ps

Response

` + "```json" + `
{
  "models": [
    {
      "name": "model-id",
      "expires_at": "2026-01-01T00:00:00Z",
      "size_vram": 1
    }
  ]
}
` + "```" + `
`
}

// TestOpenRouterFragmentDropsExamples: schema examples are not schema; the
// upstream ones name concrete models, which a Tribunus fixture must not
// carry, so the extracted fragment keeps the shape and drops every example.
func TestOpenRouterFragmentDropsExamples(t *testing.T) {
	doc := strings.Replace(syntheticOpenRouterDocument("context_length"),
		"        id:\n          type: string\n",
		"        id:\n          type: string\n          example: vendor-x/secret-model\n", 1)
	doc = strings.Replace(doc,
		"    Model:\n      type: object\n",
		"    Model:\n      type: object\n      example:\n        id: vendor-x/secret-model\n        pricing:\n          prompt: '0.1'\n", 1)
	if !strings.Contains(doc, "vendor-x/secret-model") {
		t.Fatal("test setup: examples were not planted")
	}
	got, err := openRouterFragment(doc)
	if err != nil {
		t.Fatalf("openRouterFragment() = %v", err)
	}
	if strings.Contains(got, "vendor-x") || strings.Contains(got, "example") {
		t.Fatalf("fragment keeps an example:\n%s", got)
	}
	if !strings.Contains(got, "context_length:") || !strings.Contains(got, "type: string") {
		t.Fatalf("fragment lost schema while dropping examples:\n%s", got)
	}
}
