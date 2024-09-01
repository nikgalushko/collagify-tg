# Collagify-TG

Collagify-TG is a Telegram bot written in Go that collates multiple images sent in a Telegram channel into a single collage. Each day, the bot fetches all the photos from the channel and produces a single collage from them.

The bot designed to help track daily food consumption effortlessly.

## Quick start

### Run in docker

- Build a docker image `make docker`
- Start container with `docker run --restart unless-stopped -e COLLAGIFY_TG_TOKEN=bot_token -e COLLAGIFY_BADGER_PATH=/inside/container/dbfile -v /path/to/dbfile:/inside/container/dbfile collagify-t`

### Local run

1. Make sure you have Go installed (version 1.23+ is required).

2. Build it `make build`

3. Run your new bot `COLLAGIFY_TG_TOKEN=bot_token ./collagify-tg`

Remember to set the following environment variables before running your bot:

- `COLLAGIFY_TG_TOKEN`: Your bot token from BotFather.
- `COLLAGIFY_BADGER_PATH`: Path to your Badger DB directory.

The bot uses these to connect to the Telegram API and to Badger DB.

## Contribution

Feel free to create issues and submit pull requests - contributions are welcome.

## License

Collagify-TG is available under the [MIT license](https://opensource.org/LICENSE-MIT).
