# Database Directory

This directory stores recording metadata as JSON files organized by channel name.

## Structure

Each channel gets its own subdirectory:
```
database/
├── channelname1/
│   ├── 2026-04-27.json
│   └── 2026-04-28.json
└── channelname2/
    └── 2026-04-27.json
```

## Important Notes

- This directory is automatically managed by the application
- Files are created automatically when recordings start
- Do not manually edit these files unless you know what you're doing
- This directory is git-ignored (except this README) to keep your fork clean
