<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo-light.svg" alt="yggy" height="56">
  </picture>
</p>

<h1 align="center">yggy CLI</h1>

<p align="center">Upload documents, wait for extraction, read the facts and ask questions from your terminal.</p>

## Install

You need Go 1.25 or newer.

```bash
git clone https://github.com/yggy-tech/yggy-cli
cd yggy-cli
go build -o yggy .
```

Move the `yggy` binary onto your `PATH`.

Prebuilt binaries and a Homebrew formula are not available yet.

## Log in

Create an API key in yggy under **Developers → API keys**, then:

```bash
export YGGY_API_KEY=...
yggy auth login
yggy use org
yggy use project
yggy context
```

Prefer the environment variable to the `--api-key` flag: a flag value can show up in your shell history. The CLI saves the API URL, the key and your selection in `yggy/config.json` in your user configuration folder. Only your user can read the file.

## Use it

```bash
id="$(yggy documents upload Q4-report.pdf)"
yggy documents wait "$id"
yggy documents triples "$id" > q4-facts.json
yggy query "Which contracts renew in Q1?"
```

`yggy api` lists every operation of the public API. `yggy api <operation> name=value` calls one, for example `yggy api getDocumentStatus id=<id>`. `yggy help` prints all commands.

See the [CLI guide](https://docs.yggy.tech/developers/cli/) for exit codes, scripting and CI advice, and the [quickstart](https://docs.yggy.tech/developers/quickstart/) for a first API call.

## Report problems

Open an issue in this repository, or write to [support](https://yggy.tech/support).

## License

Apache-2.0. See [LICENSE](LICENSE).
