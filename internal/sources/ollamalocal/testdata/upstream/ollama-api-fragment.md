<!--
Provenance:
Source URL: https://raw.githubusercontent.com/ollama/ollama/c2b7368d4156656ddb9a23b43f721a841b9c23e0/docs/api.md
Pinned revision: c2b7368d4156656ddb9a23b43f721a841b9c23e0
Full document sha256: 96db03080e385dbabf5d70e75f298d41b75983bc30975cae56f7119f848dd0f5
Extraction date: 2026-10-09
SPDX licence: MIT
Copyright holder: Ollama
Changes: provenance header added by Tribunus; derived /api/tags and /api/ps response shapes from examples
-->

# Ollama API response fragments

## GET /api/tags

```json
{
  "models": [
    {
      "name": "string",
      "model": "string",
      "modified_at": "string",
      "size": 0,
      "digest": "string",
      "details": {
        "parent_model": "string",
        "format": "string",
        "family": "string",
        "families": [
          "string"
        ],
        "parameter_size": "string",
        "quantization_level": "string"
      }
    }
  ]
}
```

## GET /api/ps

```json
{
  "models": [
    {
      "name": "string",
      "model": "string",
      "size": 0,
      "digest": "string",
      "details": {
        "parent_model": "string",
        "format": "string",
        "family": "string",
        "families": [
          "string"
        ],
        "parameter_size": "string",
        "quantization_level": "string"
      },
      "expires_at": "string",
      "size_vram": 0
    }
  ]
}
```
