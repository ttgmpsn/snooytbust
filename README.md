# SnooYTBust
SnooYTBust scans a subreddit for newly posted YouTube links. If it matches a blacklist, the post or comment is removed.

If you define a bot token & notification channel, the bot will also announce removed items in Slack.

Example table layout (set the table name in the config):

| `id` | `media_channel_id` (Channel ID) | `media_platform_id` (`1` for YouTube)
| --- | --- | --- |
| `1` | `UC52XYgEExV9VG6Rt-6vnzVA` | `1` |
| `2` | `UCxidp0WgNPBdIXpHZKQcoMw` | `1` |
...