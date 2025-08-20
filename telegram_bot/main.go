package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

type GeminiPayload struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

type GeminiResponse struct {
	Reply string `json:"reply"`
}

type CommandConfig struct {
	Description string `toml:"description"`
}

var (
	bot          *tgbotapi.BotAPI
	geminiURL    string
	targetChatID int64
)

func main() {
	godotenv.Load()
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	geminiURL = os.Getenv("GEMINI_ENDPOINT")
	if geminiURL == "" {
		geminiURL = "http://127.0.0.1:8765/event"
	} else if !strings.HasPrefix(geminiURL, "http://") && !strings.HasPrefix(geminiURL, "https://") {
		geminiURL = "http://" + geminiURL
	}
	if strings.HasPrefix(geminiURL, "https://") {
		geminiURL = strings.Replace(geminiURL, "https://", "http://", 1)
	}

	if chatID := os.Getenv("TARGET_CHAT_ID"); chatID != "" {
		if id, err := strconv.ParseInt(chatID, 10, 64); err == nil {
			targetChatID = id
		}
	}

	var err error
	bot, err = tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Authorized on account %s", bot.Self.UserName)
	log.Printf("Gemini endpoint: %s", geminiURL)
	log.Printf("Target chat ID: %d", targetChatID)

	if err := setBotCommands(); err != nil {
		log.Printf("Failed to set bot commands: %v", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(update.Message)
		}
	}
}

func setBotCommands() error {
	commands := []tgbotapi.BotCommand{
		{Command: "help", Description: "Displays help information"},
		{Command: "stats", Description: "Shows session token usage"},
		{Command: "save", Description: "Saves the current conversation"},
		{Command: "restore", Description: "Lists or restores a checkpoint"},
	}
	cmdDir := "../commands/listen"
	files, err := os.ReadDir(cmdDir)
	if err != nil {
		return fmt.Errorf("failed to read command directory: %w", err)
	}

	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".toml") {
			cmdName := strings.TrimSuffix(file.Name(), ".toml")
			fullPath := filepath.Join(cmdDir, file.Name())

			var config CommandConfig
			if _, err := toml.DecodeFile(fullPath, &config); err != nil {
				log.Printf("Failed to decode command config %s: %v", file.Name(), err)
				continue
			}

			if config.Description != "" {
				commands = append(commands, tgbotapi.BotCommand{
					Command:     "listen_" + cmdName,
					Description: config.Description,
				})
			}
		}
	}

	if len(commands) > 0 {
		req := tgbotapi.NewSetMyCommands(commands...)
		if _, err := bot.Request(req); err != nil {
			return fmt.Errorf("failed to set commands: %w", err)
		}
		log.Printf("Successfully set %d commands", len(commands))
	}

	return nil
}

func handleMessage(message *tgbotapi.Message) {
	if message.From.IsBot {
		return
	}

	if targetChatID != 0 && message.Chat.ID != targetChatID {
		return
	}

	text := strings.TrimSpace(message.Text)
	if text == "" {
		return
	}

	log.Printf("Processing message from %s: %s", message.From.UserName, text)

	var prompt string
	if strings.HasPrefix(text, "/listen_") {
		// It's a listen command, transform it for the Gemini CLI
		cmd := strings.Replace(text, "/listen_", "!listen:", 1)
		prompt = cmd
	} else if strings.HasPrefix(text, "/") {
		// It's a built-in command
		cmd := "!" + text
		prompt = cmd
	} else {
		// It's a regular message, wrap it in the assistant prompt
		context := ""
		if message.ReplyToMessage != nil {
			context = fmt.Sprintf("Context: %s: %s\n\n",
				message.ReplyToMessage.From.FirstName,
				message.ReplyToMessage.Text)
		}
		prompt = fmt.Sprintf("%sYou are an assistant in a Telegram chat.\nAnswer this message:\n\n%s: %s",
			context, message.From.FirstName, text)
	}

	reply := callGemini(prompt)

	msg := tgbotapi.NewMessage(message.Chat.ID, reply)
	if message.ReplyToMessage != nil {
		msg.ReplyToMessageID = message.MessageID
	}

	if _, err := bot.Send(msg); err != nil {
		log.Printf("Error sending message: %v", err)
	}
}

func callGemini(prompt string) string {
	payload := GeminiPayload{
		Source:  "telegram",
		Message: prompt,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Error marshaling JSON: %v", err)
		return "❌ Error processing request"
	}

	client := &http.Client{Timeout: 300 * time.Second}
	log.Printf("Calling Gemini at URL: %s", geminiURL)
	resp, err := client.Post(geminiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error calling Gemini: %v", err)
		return fmt.Sprintf("❌ Error from Gemini server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Gemini returned status %d", resp.StatusCode)
		return fmt.Sprintf("❌ Gemini server error: %d", resp.StatusCode)
	}

	var geminiResp GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		log.Printf("Error decoding response: %v", err)
		return "❌ Error parsing response"
	}

	if geminiResp.Reply == "" {
		return "No reply."
	}

	return geminiResp.Reply
}
