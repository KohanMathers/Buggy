# Buggy

A self-hosted bug tracker that you configure entirely from YAML and can be shpped from a single static binary or a Docker container. Point it at Postgres when you outgrow SQLite, or like making infraslop.

Buggy is designed for public trackers where many people report the same bug. It has duplicate detection while you type, crash-log parsing and fingerprinting, "I have this too" confirmations, and statuses that are explained in plain language. Every field, workflow, form, role and string is defined in config.

---

## Features

**Reporting**
- Step-by-step report forms defined in YAML.
- Duplicate suggestions while you type. Full-text search plus title similarity and crash-signature matching. If a suggestion is itself a duplicate, Buggy points you to the original issue.
- Log & crash parsers (regex rules in YAML). They fill in fields such as version, OS and mod loader, add labels, and compute a crash fingerprint so the same crash is grouped automatically. A live preview shows what was detected before you submit.
- "I have this too" / "Still happens in X". One click adds your vote, version and environment. A confirmation on a newer version than the fix reopens a "fixed" issue for triage.
- "Who's affected" summary: a version and environment breakdown aggregated from the reporter and everyone who confirmed.
- Plain-language status explanations: each state and resolution has configurable text, e.g. "This is a duplicate of APP-12. Your vote was moved there.".

**Triage & moderation**
- Mark duplicate moves votes, watchers and confirmations, plus the duplicate reporter's environment, to the original issue.
- Configurable workflows for states, categories, transitions, per-transition permissions, reporter-only transitions, required comments and allowed resolutions.
- *uto-reopen when the reporter replies to an "awaiting response" issue (`on_reporter_reply`).
- Canned responses, bulk actions, a triage queue, staff-only comments, keyboard shortcuts, a "possible duplicates" sidebar and assignees.
- Automations for scheduled or event-triggered rules using the search language, e.g. "close *awaiting response* after 14 days idle".
- Anti-spam: per-action rate limits, Turnstile, hCaptcha or reCAPTCHA, first-post moderation, blocked words, minimum account age, shadow-bans and suspensions.

**Everything else**
- Projects, versions, labels (private and restricted labels), custom fields (text, textarea, number, select, multiselect, version, user, bool, date, url), and staff-only fields.
- Roles with permission inheritance and wildcards, per-project members and private projects.
- Votes, watchers, @mentions, in-app and email notifications, "trending" and "most affected" views, per-version changelogs, and user profiles with reputation.
- Local accounts plus GitHub, Discord, Google, Microsoft, GitLab, or any OIDC/OAuth2 provider.
- REST API with tokens, outgoing webhooks (JSON, Discord, Slack), git push webhooks (`fixes APP-12` closes issues), and Atom feeds.
- Importers for Jira and GitHub Issues.
- Portable backups (SQLite ⇄ Postgres), Prometheus `/metrics`, `/healthz`, and config hot reload.
- Theme colours, logo, custom CSS, `theme_dir` template overrides, UI string overrides and translations.

## Quick start

### Single binary

```sh
go install github.com/kohanmathers/buggy/cmd/buggy@latest   # or build from a clone:
make build                   # or: go build -o buggy ./cmd/buggy
./buggy init                 # writes config.yml (optional, serve does it too)
./buggy serve                # http://localhost:8080
```

The first account you register becomes the admin (`auth.first_user_admin`). You can also create users from the CLI:

```sh
./buggy user create alice --password 'secret123' --role admin
```

### Docker

```sh
docker compose up -d         # http://localhost:8080
```

The config lives in `./config/config.yml` (it's created on first start if the directory is writable by UID 10001; otherwise run `buggy init` yourself) and data in the `buggy-data` volume. To use Postgres, set `BUGGY_DATABASE_DRIVER=postgres` and `BUGGY_DATABASE_DSN`; see `docker-compose.yml`.

## Editing the HTML (dev mode)

Templates and static files are separate files under `web/`. They are embedded into the binary at build time:

```
web/
  templates/layout.html          # page shell
  templates/partials/*.html      # shared pieces (issue rows, badges, inputs…)
  templates/pages/*.html         # one file per page
  static/app.css, app.js         # styling + progressive enhancement
```

While developing, run:

```sh
./buggy serve --dev              # or: make dev
```

In dev mode Buggy reads `web/` from disk on every request, so you can edit HTML, CSS and JS and just refresh. Template errors are shown in the browser. Use `--web-dir` if you run from somewhere else. Rebuild to embed your changes into the binary.

To customise a production install without rebuilding, set `server.theme_dir: ./theme` and put override files at the same paths, e.g. `theme/templates/pages/home.html` or `theme/static/app.css`. Files there take priority over the embedded ones.

## Configuration

Everything has a default (`buggy config defaults` prints them), so a config only needs what you change. Some general rules:

- `${ENV}` and `${ENV:-fallback}` are expanded from the environment.
- Every `config.d/*.yml` next to `config.yml` is merged in: maps merge, lists append. This makes it easy to ship "packs", e.g. `examples/config.d/modding.yml`.
- Unknown keys are errors, so typos are caught. `buggy config validate` explains every problem.
- A running server hot-reloads the config. An invalid config is rejected and the previous one keeps running and the admin page shows the error.

The starter config (`buggy init`) is a complete, commented example. Here is an overview of each section:

```yaml
instance:
  name: Buggy                            # used in page titles and emails
  title: Acme Bug Tracker                # text next to the logo (default: name; "-" hides it)
  description: Report bugs, vote on what matters
  url: https://bugs.example.com          # used in links, emails, OAuth callbacks
  logo: /static/logo.png                 # image URL/path; unset or broken -> plain colour square
  default_avatar: https://upload.wikimedia.org/wikipedia/commons/a/ac/Default_pfp.jpg   # fallback profile picture
  theme:
    primary: "#0c66e4"
    accent: "#f59e0b"
    mode: auto                           # auto | light | dark
    radius: 3px
    font: Inter, system-ui, sans-serif
    mono_font: "JetBrains Mono", monospace
    font_size: 14px
    font_url: https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap
    colors:                              # light mode, see "Theme colours" below
      topbar: "#1d2125"
      topbar_text: "#ffffff"
    dark_colors:                         # dark mode
      background: "#101214"
  custom_css: ".brand { letter-spacing: 1px }"
  custom_css_file: ./custom.css
  announcement: "**2.0 is out!** Please re-test open reports."   # markdown banner
  footer_links: [{ label: Discord, url: "https://discord.gg/…" }]
  default_locale: en
  issues_per_page: 50

server:
  listen: ":8080"
  dev: false                 # same as --dev
  web_dir: web               # used in dev mode
  theme_dir: ""              # template/static overrides
  data_dir: ./data
  secret_key: ""             # optional; auto-generated and stored in the DB
  trust_proxy: false         # honour X-Forwarded-For
  cookie_secure: auto
  metrics: true
  metrics_token: ""          # require "Authorization: Bearer <token>" for /metrics
  automation_interval: 5m

database:
  driver: sqlite             # or postgres
  dsn: ""                    # sqlite default: <data_dir>/buggy.db

storage:
  driver: local              # or s3 (any S3-compatible: AWS, MinIO, R2, B2…)
  path: ""                   # default <data_dir>/attachments
  max_size: 25MB
  allowed_types: []          # e.g. [".png", ".log", ".txt", ".zip"]
  s3: { endpoint: s3.amazonaws.com, bucket: buggy, region: eu-west-1, access_key: "…", secret_key: "…", use_ssl: true }
```

### Theme colours

`instance.theme.colors` overrides colours in light mode, and `instance.theme.dark_colors` overrides them in dark mode. Values can be `#rrggbb`, `rgb(...)`, `hsl(...)` or a CSS colour name. Any key you leave out keeps the built-in value.

| Key | Used for |
|---|---|
| `primary`, `accent` | brand colour (buttons, links, active items), accent |
| `background`, `background_sunken` | page background, inputs and sunken areas |
| `surface`, `surface_alt` | cards and panels, secondary surfaces |
| `hover`, `selected` | hover and selected highlights |
| `border`, `border_strong` | borders |
| `text`, `text_subtle`, `text_muted` | main, secondary and faint text |
| `link` | links |
| `success`, `warning`, `danger`, `info` | feedback colours, info panels |
| `topbar`, `topbar_text` | top navigation bar |
| `sidebar`, `sidebar_text` | project / filter sidebar |
| `status_todo`, `status_active`, `status_waiting`, `status_done`, `status_danger` (plus `_text` for each) | status lozenges |

For anything beyond colours and fonts, use `custom_css` or a `theme_dir`.

### Auth & roles

```yaml
auth:
  local: { enabled: true, registration: open, min_password: 8 }
  first_user_admin: true
  admins: [alice]            # always admins
  default_role: user
  anonymous_role: anonymous
  session_ttl: 30d
  providers:
    - { id: github,  type: github,  client_id: "${GH_ID}", client_secret: "${GH_SECRET}" }
    - { id: discord, type: discord, client_id: "…", client_secret: "…" }
    - { id: google,  type: google,  client_id: "…", client_secret: "…" }
    - { id: sso, type: oidc, name: "Company SSO", issuer: https://auth.example.com, client_id: "…", client_secret: "…", default_role: helper }
    - id: custom
      type: oauth2
      auth_url: https://…/authorize
      token_url: https://…/token
      userinfo_url: https://…/me
      id_field: id
      username_field: login
```

The OAuth callback URL is `<instance.url>/auth/<id>/callback`.

Roles inherit from each other and accept wildcards (`issue.*`, `*`):

```yaml
roles:
  helper:
    label: Helper
    badge: Helper              # shown next to names
    color: "#10b981"
    inherits: [user]
    permissions: [triage, issue.link, issue.duplicate, issue.label, canned.use]
```

The defaults are `anonymous`, `user`, `helper`, `moderator`, `developer` and `admin`. These are the available permissions:

| Permission | Allows |
|---|---|
| `issue.view` / `issue.view_private` | see issues / private issues |
| `issue.create`, `issue.edit_own`, `issue.edit`, `issue.delete` | report, edit own, edit any, delete |
| `issue.transition`, `triage` | default permissions for workflow transitions |
| `issue.assign`, `issue.label`, `issue.link`, `issue.duplicate`, `issue.bulk` | triage tools |
| `issue.vote`, `issue.watch`, `issue.confirm` | community actions |
| `comment.create`, `comment.edit_own`, `comment.edit`, `comment.delete`, `comment.restricted` | comments (restricted = staff-only comments) |
| `attachment.upload`, `attachment.delete` | files |
| `canned.use`, `moderation`, `version.manage`, `user.manage`, `admin` | staff |
| `api.token`, `search.save`, `search.save_public` | misc |

### Projects, fields, labels

```yaml
projects:
  - key: APP                  # issue keys become APP-1, APP-2…
    name: My App
    icon: https://example.com/app.png   # image; unset or broken -> plain square in `color`
    color: "#0c66e4"
    workflow: default
    form: bug_report
    fields: [crash_log]       # extra fields (fields without `projects:` apply everywhere)
    versions: [{ name: "1.0", released: true }, "1.1"]
    private: false            # private projects are visible to members only
    members: { alice: developer }   # per-project roles
    private_by_default: false
    default_assignee: ""
    archived: false

fields:
  - id: platform
    label: Platform
    type: select              # text textarea number select multiselect version user bool date url
    options: [{ value: windows, label: Windows, color: "#0078d4" }, linux]
    required: true
    environment: true         # counted in "who's affected", asked on "I have this too"
    show_in_list: true        # badge in issue lists
    edit_permission: triage   # staff-only field
    private: false            # only visible with issue.view_private
    pattern: '^\d+\.\d+'      # validation for text fields

labels:
  - { name: security, color: "#7c3aed", private: true }       # makes issues private
  - { name: needs-retest, restricted: true }                  # only issue.label can set
```

### Workflows

```yaml
workflows:
  default:
    initial: open
    duplicate_state: resolved
    duplicate_resolution: duplicate
    states:
      - id: awaiting_response
        label: Awaiting Response
        category: waiting          # open | active | waiting | closed
        color: "#f59e0b"
        on_reporter_reply: open    # reporter comment moves it back
        explain: We need more info from the reporter…
    resolutions:
      - id: duplicate
        label: Duplicate
        valid: false               # doesn't count towards reporter reputation
        explain: This is a duplicate of {{.Duplicate}}. Your vote was moved there.
    transitions:
      - id: request_info
        label: Request info
        from: [open, confirmed]    # or ["*"]
        to: awaiting_response
        permission: triage
        require_comment: true
      - id: withdraw
        label: Close my report
        to: resolved
        reporter: true             # the reporter may use it
        resolutions: [withdrawn]
```

Status lozenges are coloured by state category, the way Jira does it: grey for `open`, yellow for `waiting`, blue for `active` and green for `closed`. A state's `color` is used for Discord webhook embeds.

The `explain` text is Markdown with template variables: `{{.Duplicate}}`, `{{.FixVersions}}`, `{{.Reporter}}`, `{{.Instance}}` and `{{.Issue.Key}}`.

### Forms

```yaml
forms:
  bug_report:
    intro: "**Search first!**"
    duplicate_check: true
    steps:
      - title: What happened?
        fields:
          - title
          - field: description
            template: "**Steps to reproduce**\n1.\n"
          - category
      - title: Logs
        fields:
          - field: crash_log
            required: true
            show_if: { field: category, equals: crash }   # also: in: [...], not_empty: true
          - attachments
```

The built-in form fields are `title`, `description`, `affects_versions`, `attachments`, `labels` and `private`.

### Parsers

Parsers run over the description, textarea fields and text attachments, both at creation and on later uploads:

```yaml
parsers:
  - id: java-stacktrace
    match:
      filename: ["*.log", "crash-*.txt"]     # optional
      content: '(?m)^\s+at [\w.$]+\('        # optional regex
    labels: [crash]
    extract:
      - { field: runtime_version, regex: 'Java Version: (\S+)' }
      - { field: platform, regex: '(?i)(windows|linux)', map: { Windows: windows } }
      - { field: mods, regex: '(?m)^\t\t(\w+): ', all: true }
    fingerprint:
      exception: '(?m)^([\w.$]+(?:Exception|Error))'
      frames: '(?m)^\s+at ([\w.$<>]+)\('
      count: 5
      ignore: ['^java\.lang\.reflect']
```

### Automations

```yaml
automations:
  - id: close-unanswered
    on: [schedule]                 # or events: issue.created, issue.commented, issue.transitioned,
                                   # issue.updated, issue.attachment, issue.duplicate, issue.voted, issue.confirmed, issue.linked
    query: "status:awaiting_response"
    idle: 14d
    do:
      transition: resolved         # target state
      resolution: incomplete
      comment: "Closing: no reply in 14 days, {{.Reporter}}."
      add_labels: [stale]
      remove_labels: []
      set_fields: { priority: low }
      assign: alice
      private: false
```

Scheduled rules run at most once per issue per update, so they never loop.

### Canned responses, notifications, spam, text

```yaml
canned_responses:
  - { id: need-logs, name: Ask for logs, body: "Please attach your log.", transition: request_info }

notifications:
  mentions: true
  email: { enabled: true, host: smtp.example.com, port: 587, username: "…", password: "…", from: "Buggy <bugs@example.com>", tls: starttls }

spam:
  rate_limits: { issue: 10, comment: 60, register: 5, upload: 40, vote: 300 }   # per hour
  captcha: { provider: turnstile, site_key: "…", secret: "…", on: [register, issue] }
  first_post_moderation: true
  blocked_words: [casino]
  min_account_age: 10m

reputation: { report_valid: 10, report_fixed: 15, duplicate_found: 3, confirmation: 1, comment: 0 }

text:                       # override any UI string
  nav.report: "File a bug"
locales:
  de: { nav.report: "Fehler melden", nav.issues: "Tickets" }
```

### Who is a helper, moderator or admin?

A role is just a named set of permissions under `roles:`. The defaults build on each other:

| Role | Inherits | Adds |
|---|---|---|
| `anonymous` | | view issues |
| `user` | anonymous | report, comment, vote, watch, confirm, upload |
| `helper` | user | `triage` (confirm, request info, resolve as invalid/duplicate…), link, mark duplicates, labels, canned replies, staff-only comments |
| `moderator` | helper | edit anything, all transitions, assign, private issues, bulk actions, delete comments/files, account moderation |
| `developer` | moderator | manage versions, delete issues |
| `admin` | | everything (`*`) |

Each user has one global role, stored in the database. To give someone a role:

- Web: Administration -> Users (role dropdown), or the role selector on their profile. You need `user.manage`.
- CLI: `buggy user role alice moderator`.
- Config: `auth.admins: [alice]` makes someone an admin on every login. `auth.providers[].default_role` sets the role for new OAuth sign-ups. `projects[].members: { bob: helper }` grants a role in one project only.
- The first account registered becomes `admin` (`auth.first_user_admin`).

You can rename the roles, change their badges and colours, or define your own, e.g. a `triager` role with only `triage` and `issue.duplicate`.

### Account controls

Moderators can manage accounts from Account actions on a user's profile, via `POST /api/v1/users/{name}/moderation`, or with `buggy user <action> <name>`:

| Action | Effect |
|---|---|
| `kick` | Signs the user out of every session. Their account still works. |
| `lock` | Suspends the account for a duration (`--for 7d`), then unlocks it automatically. |
| `ban` | Suspends the account permanently. |
| `unban` | Lifts a lock or ban. |
| `mute` | The user can log in, read and vote, but can't post issues, comments or files. Optionally time-limited. |
| `unmute` | Lifts a mute. |
| `shadowban` / `unshadowban` | Their content is only visible to themselves and staff. |
| `trust` / `untrust` | Trusted users skip first-post moderation and captchas. |
| `revoke_tokens` | Deletes all of the user's API tokens. |

Locks, bans and kicks sign the user out immediately. Suspended users see the reason when they try to log in, and muted users see it where they'd comment. Every action is recorded in the user's moderation history. Moderators can't act on other staff accounts (that needs `user.manage`), and only admins can act on admins.

### Recovery codes (forgotten passwords)

Buggy doesn't need email to recover accounts. When someone signs up with a password, they're shown 10 single-use recovery codes. They can generate a new set at any time under Account settings -> Recovery codes. If they forget their password, Log in -> Forgot password? lets them set a new one with their username and one unused code. That signs them in and revokes every other session. Codes are stored hashed, and attempts are rate-limited. Admins can also issue codes with `buggy user recovery-codes <name>`, or set a password with `buggy user password <name>`.

## Search syntax

```
crash map status:open,confirmed label:crash -label:wontfix
version:>=1.2 fixed:1.3 votes:>10 created:<7d updated:>30d
reporter:me assignee:none voted:me watching:me is:open is:private
has:attachment has:fingerprint fingerprint:ab12cd platform:windows sort:votes
```

Any custom field works as a key (`<field>:value`, `<field>:none`, and `<field>:>5` for number and date fields). `/help` in the app lists every key.

## REST API

`GET /api/v1` lists the endpoints. Authenticate with a token from Settings -> API tokens:

```sh
curl -H "Authorization: Bearer bgy_…" "https://bugs.example.com/api/v1/issues?q=is:open+sort:votes"
curl -H "Authorization: Bearer bgy_…" -H "Content-Type: application/json" \
     -d '{"project":"APP","title":"Crash on start","fields":{"platform":"linux"}}' \
     https://bugs.example.com/api/v1/issues
# Create a version from CI on release:
curl -H "Authorization: Bearer bgy_…" -d '{"name":"1.4.0","released":true}' https://…/api/v1/projects/APP/versions
```

## Webhooks & git integration

```yaml
webhooks:
  - name: discord
    url: https://discord.com/api/webhooks/…
    format: discord             # json | discord | slack
    events: [issue.created, issue.transitioned]   # or ["*"]
    projects: [APP]
    secret: "…"                 # json: X-Buggy-Signature: sha256=<hmac>
    include_private: false

git:
  - id: github
    provider: github            # github | gitlab | gitea | generic
    secret: "${GIT_WEBHOOK_SECRET}"
    projects: [APP]
    close_keywords: [fixes, closes, resolves]
    close_resolution: fixed
```

Point your repository's push webhook at `<url>/hooks/git/<id>`. Commits that mention `APP-12` are linked to the issue, and `fixes APP-12` resolves it.

## CLI

```
buggy serve [--dev] [--web-dir DIR] [--listen ADDR]
buggy init [path]
buggy config validate | defaults | show
buggy user create <name> [--password P] [--email E] [--role R]
buggy user role <name> <role>
buggy user password <name> --password P
buggy backup <file.tar.gz> / buggy restore <file.tar.gz>
buggy import jira --url URL --jql "project = MC" --project APP
buggy import github --repo owner/name --project APP [--token T]
buggy reindex | automations run | test-email <addr> | version
```

Set `BUGGY_LOG=debug` for verbose logs and `BUGGY_LOG_FORMAT=json` for JSON logs.

## Backups, migration & imports

- `buggy backup out.tar.gz` writes every table as JSON lines, plus attachments (from local disk *or* S3) and your config files.
- `buggy restore out.tar.gz` loads a backup into an empty database of either driver. Use it to move from SQLite to Postgres: point the config at Postgres, then run `buggy restore`.
- Importers keep issue numbers, dates, comments, votes, versions, labels and duplicate links. Imported users get placeholder accounts. Re-running an import skips issues that already exist. Use `--status-map` and `--resolution-map` to map source names onto your workflow.

## Project layout

```
cmd/buggy/            CLI entrypoint
internal/config/      YAML schema, defaults, validation, hot reload
internal/store/       SQLite/Postgres persistence, migrations, full-text search
internal/search/      query language -> SQL
internal/app/         business logic: permissions, workflow, duplicates, automations, notifications, webhooks, git
internal/server/      HTTP: pages, REST API, auth/OAuth, feeds, metrics
internal/parsers/     log/crash parsers and fingerprints
internal/importer/    Jira & GitHub importers
internal/backup/      portable backup/restore
web/                  templates + static files (embedded; live-reloaded with --dev)
examples/config.d/    optional config packs (modding)
```

Run the tests with `go test ./...`. To run the store and end-to-end tests against Postgres as well, set `BUGGY_TEST_PG_DSN=postgres://…`.
