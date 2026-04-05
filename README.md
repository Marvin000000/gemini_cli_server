# GeminiCLI Telegram Bot

This project provides a Telegram bot integration for the Gemini CLI, enabling powerful AI-driven automation and interactive responses directly within your Telegram chats. The core idea is to bridge the gap between conversational interfaces and robust command-line automation, allowing Gemini to participate in conversations, respond to messages, and automate tasks in a helpful and informative way.

## Installation

To get started, you'll need to clone this repository and set up the necessary components.

### Cloning the Repository

First, clone the repository to your local machine:

```bash
git clone https://github.com/bravian1/gemini_cli_server.git
```

### Project Setup

This project assumes you have the `gemini` CLI installed and available in your PATH.

## Telegram Bot Setup

To get your Telegram bot up and running:

1.  **Create a bot with [@BotFather](https://t.me/botfather) on Telegram.** Follow the instructions to create a new bot and obtain your unique bot token.
2.  **Get your bot token.** This token is essential for your `main.go` application to authenticate with the Telegram API.
3.  **Set up environment variables in `.env`:**

    ```bash
    TELEGRAM_BOT_TOKEN=your_bot_token_here
    GEMINI_API_KEY=your_gemini_api_key   # Optional: For voice transcription
    TARGET_CHAT_ID=                      # Optional: Specific chat ID for restricted access
    ```
    *   `TELEGRAM_BOT_TOKEN`: The token you received from BotFather.
    *   `GEMINI_API_KEY`: (Optional) If you want the bot to handle voice messages, provide your Gemini API key.
    *   `TARGET_CHAT_ID`: (Optional) If set, the bot will only respond to messages from this specific chat ID.

## Usage: Bot Commands

The bot handles several built-in slash commands internally without passing them to Gemini:

*   `/pwd` - Displays the current working directory of the bot.
*   `/cd <path>` - Changes the working directory of the bot.
*   `/status` - Shows the current status of the bot (Idle/Busy) and the current session ID.
*   `/new` - Resets the Gemini session, starting a fresh conversation on the next message.
*   `/ls` - Lists the contents of the current directory.

While Gemini is processing a request, the bot will show a "busy" status (typing...) on Telegram.

### Running the Telegram Bot

To start your Telegram bot, run the `main.go` application from the root directory:

```bash
go run main.go
```

## MCP Integration
If your Gemini CLI is integrated with MCP servers they are fully accessible via the Telegram bot. Meaning Gemini CLI will invoke those MCP servers when a message is received if they will help respond to the message.
