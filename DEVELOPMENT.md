# Gambit.Golf Local Development Setup

This is your local development environment for the gambit.golf project, forked from njump.me.

## Project Overview

**Gambit.golf is the web portal component of your NOGA Golf App ecosystem.** It serves as a comprehensive golf-focused web platform that complements your React Native mobile app by providing:

- **Tournament Sharing**: SEO-optimized pages for viral golf tournament sharing
- **Premium Purchases**: Bypass App Store fees (100% vs 70% revenue retention)
- **Course Discovery**: Comprehensive golf course database with rich media
- **Social Media Engine**: Rich previews that drive organic user acquisition
- **B2B Portal**: White-label solutions for golf clubs and corporate clients

### Technical Foundation

Built on the proven njump architecture with golf-specific enhancements:

- **Backend**: Go 1.24.2
- **Templates**: templ (Go templating)
- **Styling**: Tailwind CSS
- **Database**: BadgerDB and LeafDB
- **Task Runner**: just
- **Live Reload**: air

## Quick Start

### 1. Development Server
```bash
# Start development server with live reload
just dev

# Or manually:
TAILWIND_DEBUG=true PORT=3001 go run .
```

### 2. Building
```bash
# Build the project
just build

# Or manually:
templ generate && go build -o njump
```

### 3. Available Commands

```bash
just --list                    # Show all available commands
just dev                       # Start development server with live reload
just build                     # Build the project
just templ                     # Generate templates
just tailwind                  # Build Tailwind CSS
just prettier                  # Format templates
```

## Development Workflow

1. **Make changes** to Go files, templates (.templ), or CSS
2. **Templates are auto-generated** when using `just dev`
3. **Live reload** will restart the server automatically
4. **Access the app** at http://localhost:3001

## Project Structure

```
├── main.go                    # Main application entry point
├── *.templ                    # Template files (generate Go code)
├── *.go                       # Go source files
├── base.css                   # Tailwind CSS input file
├── static/                    # Static assets
├── justfile                   # Task definitions
├── go.mod                     # Go dependencies
└── package.json               # Node.js dependencies (Tailwind)
```

## Environment Variables

```bash
PORT=3001                      # Server port
DOMAIN=localhost               # Domain name
TAILWIND_DEBUG=true            # Enable Tailwind debug mode
DISK_CACHE_PATH=/tmp/njump-internal
EVENT_STORE_PATH=/tmp/njump-db
```

## Git Setup

Your repository is set up with:
- **origin**: https://github.com/DocNR/gambit.golf.git (your fork)
- **upstream**: https://github.com/fiatjaf/njump.git (original repo)

### Keeping in sync with upstream:
```bash
git fetch upstream
git checkout master
git merge upstream/master
git push origin master
```

## Supported Nostr Event Types

- kind 0: Metadata (profiles)
- kind 1: Short Text Notes
- kind 6: Reposts
- kind 11: Threads
- kind 1111: Comments
- kind 1063: File Metadata
- kind 30023: Long-form Content
- kind 30311: Live Events
- kind 30818: Wiki Articles
- kind 31922/31923: Calendar Events
- And more...

## Development Tips

1. **Template Changes**: When you modify `.templ` files, run `templ generate` or use `just dev`
2. **CSS Changes**: Modify `base.css` and run `just tailwind` or use `just dev`
3. **Live Reload**: Use `air -c .air.toml` for advanced live reload configuration
4. **Testing**: Access different Nostr entities by appending them to the URL (e.g., `localhost:3001/npub1...`)

## Troubleshooting

- **Build Errors**: Make sure `templ generate` has been run
- **CSS Not Loading**: Check that `static/tailwind-bundle.min.css` exists
- **Port Conflicts**: Change the PORT environment variable
- **Dependencies**: Run `go mod download` and `npm install`

## Next Steps

1. Explore the codebase and understand the routing in `main.go`
2. Look at template files to understand the UI structure
3. Check out the original njump.me documentation for more details
4. Start making your customizations for gambit.golf!

---

Happy coding! 🚀
