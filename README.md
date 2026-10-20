<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo-light.svg" alt="yggy" height="56">
  </picture>
</p>

<h1 align="center">yggy CLI</h1>

<p align="center">Upload documents, wait for extraction, read the facts and ask questions from your terminal.</p>

## Install

On macOS and Linux:

```bash
curl -fsSL https://github.com/yggy-tech/yggy-cli/releases/latest/download/install.sh | sh
```

The script downloads the latest release for your system (amd64 or arm64), checks it against the SHA-256 checksum published with the release and installs `yggy` to `~/.local/bin`. Read [install.sh](install.sh) first if you like. Set `YGGY_VERSION=0.1.0` to pick a release, or `YGGY_INSTALL_DIR` to install somewhere else.

On Windows, download the zip for your system from the [releases page](https://github.com/yggy-tech/yggy-cli/releases) and put `yggy.exe` on your `PATH`. An installer for Windows and a Homebrew formula are coming soon.

To build it yourself you need Go 1.25 or newer:

```bash
git clone https://github.com/yggy-tech/yggy-cli
cd yggy-cli
go build -o yggy .
```

Check the result with `yggy version`. Update later with `yggy update`; it checks the release checksum too. `yggy update --check` only reports whether a newer release exists.

## Log in

```bash
yggy auth login
```

The default is your browser. The CLI prints a one-time code and opens yggy; sign in, check that the page shows the same code and approve. The CLI then stores an API key that yggy created for you. It can read and upload documents and ask questions, never more than your role allows, and it expires after 90 days. You can revoke it under **Developers → API keys**. Only approve a code you started yourself. Add `--no-browser` to print the address instead of opening it, for example over SSH.

For scripts and CI, use an API key from **Developers → API keys** instead:

```bash
export YGGY_API_KEY=...
yggy auth login
```

Prefer the environment variable to the `--api-key` flag: a flag value can show up in your shell history. Without a terminal the CLI never opens a browser.

Then pick where to work:

```bash
yggy use org
yggy use project
yggy context
```

The CLI saves the API URL, the key and your selection in `yggy/config.json` in your user configuration folder. Only your user can read the file.

## Use it

```bash
id="$(yggy documents upload Q4-report.pdf)"
yggy documents wait "$id"
yggy documents triples "$id" > q4-facts.json
yggy ask "Which contracts renew in Q1?"
yggy query "contract renewal Q1"
```

`yggy ask` writes an answer and lists the documents it used. `yggy chat` keeps a conversation going so you can ask follow-ups. `yggy query` only finds the matching documents and ranks them. `yggy orgs`, `yggy projects` and `yggy documents list` show what you can work with. `yggy <command> --help` prints the options of one command.

`yggy api` lists every operation of the public API. `yggy api <operation> name=value` calls one, for example `yggy api getDocumentStatus id=<id>`. `yggy help` prints all commands.

See the [CLI guide](https://docs.yggy.tech/developers/cli/) for exit codes, scripting and CI advice, and the [quickstart](https://docs.yggy.tech/developers/quickstart/) for a first API call.

## Report problems

Open an issue in this repository, or write to [support](https://yggy.tech/support).

## License

Apache-2.0. See [LICENSE](LICENSE).
