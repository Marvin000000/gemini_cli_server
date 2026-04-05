package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

type CommandConfig struct {
	Description string `toml:"description"`
}

type UserState struct {
	State         string
	LastSessionID string
	IsProcessing  bool
}

var (
	bot          *tgbotapi.BotAPI
	geminiAPIKey string
	targetChatID int64
	userStates   = make(map[int64]*UserState)
	statesMu     sync.RWMutex
	envFilePath  = ".env"
	chatsDir     = "/home/dev/.gemini/tmp/gemini-cli-server/chats"
)

const (
	maxVoiceDurationSeconds = 300              // 5 minutes
	maxVoiceFileSizeBytes   = 50 * 1024 * 1024 // 50MB
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: .env file not found, will create one if needed")
	}

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	geminiAPIKey = os.Getenv("GEMINI_API_KEY")

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
	log.Printf("Target chat ID: %d", targetChatID)

	if geminiAPIKey != "" {
		log.Printf("Gemini API key loaded from environment")
	} else {
		log.Printf("No Gemini API key found - voice transcription will require user input")
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

func getUserState(userID int64) *UserState {
	statesMu.Lock()
	defer statesMu.Unlock()
	if state, exists := userStates[userID]; exists {
		return state
	}
	userStates[userID] = &UserState{}
	return userStates[userID]
}

func handleMessage(message *tgbotapi.Message) {
	if message.From.IsBot {
		return
	}

	if targetChatID != 0 && message.Chat.ID != targetChatID {
		return
	}

	// Handle user state for API key setup
	userState := getUserState(message.From.ID)
	if userState.State == "waiting_api_key" {
		handleAPIKeyInput(message)
		return
	}

	// Handle voice messages
	if message.Voice != nil {
		handleVoiceMessage(message)
		return
	}

	text := strings.TrimSpace(message.Text)
	if text == "" {
		return
	}

	log.Printf("Processing message from %s: %s", message.From.UserName, text)

	// Handle slash commands
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text)
		command := parts[0]
		
		switch command {
		case "/pwd":
			cwd, _ := os.Getwd()
			bot.Send(tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Current directory: %s", cwd)))
			return
		case "/ls":
			files, err := os.ReadDir(".")
			if err != nil {
				bot.Send(tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Error reading directory: %v", err)))
				return
			}
			var builder strings.Builder
			builder.WriteString("Contents of current directory:\n")
			for _, file := range files {
				if file.IsDir() {
					builder.WriteString("📁 " + file.Name() + "/\n")
				} else {
					builder.WriteString("📄 " + file.Name() + "\n")
				}
			}
			bot.Send(tgbotapi.NewMessage(message.Chat.ID, builder.String()))
			return
		case "/cd":
			if len(parts) < 2 {
				bot.Send(tgbotapi.NewMessage(message.Chat.ID, "Usage: /cd <path>"))
				return
			}
			path := parts[1]
			if err := os.Chdir(path); err != nil {
				bot.Send(tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Error changing directory: %v", err)))
				return
			}
			cwd, _ := os.Getwd()
			bot.Send(tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Changed directory to: %s", cwd)))
			return
		case "/new":
			userState.LastSessionID = ""
			bot.Send(tgbotapi.NewMessage(message.Chat.ID, "✅ Gemini session reset. Next message will start a new session."))
			return
		case "/status", "/state":
			status := "Idle"
			if userState.IsProcessing {
				status = "Busy (Executing Gemini)"
			}
			sessionID := userState.LastSessionID
			if sessionID == "" {
				sessionID = "None (New session will be created)"
			}
			bot.Send(tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Status: %s\nSession ID: %s", status, sessionID)))
			log.Printf("User %s requested status: Status=%s, SessionID=%s", message.From.UserName, status, sessionID)
			return
		}
	}

	userState.IsProcessing = true
	defer func() { userState.IsProcessing = false }()

	// Start typing indicator
	stopTyping := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			bot.Send(tgbotapi.NewChatAction(message.Chat.ID, tgbotapi.ChatTyping))
			select {
			case <-ticker.C:
			case <-stopTyping:
				return
			}
		}
	}()
	defer close(stopTyping)

	var prompt string
	
		
		context := ""
		if message.ReplyToMessage != nil {
			context = fmt.Sprintf("Context: %s: %s\n\n",
				message.ReplyToMessage.From.FirstName,
				message.ReplyToMessage.Text)
		}
		prompt = fmt.Sprintf("%sYou are an assistant in a Telegram chat.\nAnswer this message:\n\n%s: %s",
			context, message.From.FirstName, text)
	

	reply, newSessionID := callGemini(prompt, userState.LastSessionID)
	if newSessionID != "" {
		userState.LastSessionID = newSessionID
	}

	msg := tgbotapi.NewMessage(message.Chat.ID, reply)
	if message.ReplyToMessage != nil {
		msg.ReplyToMessageID = message.MessageID
	}

	if _, err := bot.Send(msg); err != nil {
		log.Printf("Error sending message: %v", err)
	}
}

func callGemini(prompt string, sessionID string) (string, string) {
	var args []string
	if sessionID != "" {
		log.Printf("[Gemini] Running: BUSY, SessionID: %s", sessionID)
		args = []string{"--yolo", "-r", sessionID, "-p", prompt}
	} else {
		log.Printf("[Gemini] Running: BUSY, SessionID: NEW")
		args = []string{"--yolo", "-p", prompt}
	}

	cmd := exec.Command("gemini", args...)
	output, err := cmd.CombinedOutput()
	if err != nil && sessionID != "" {
		log.Printf("[Gemini] Error resuming session %s: %v. Retrying with new session.", sessionID, err)
		return callGemini(prompt, "")
	} else if err != nil {
		log.Printf("[Gemini] Error executing: %v, output: %s", err, string(output))
		return fmt.Sprintf("❌ Error executing gemini: %v\n\nOutput:\n%s", err, string(output)), ""
	}

	newSessionID := findLatestSessionID()
	log.Printf("[Gemini] Running: IDLE, SessionID: %s", newSessionID)
	reply := string(output)
	if strings.TrimSpace(reply) == "" {
		return "No reply received from Gemini.", newSessionID
	}

	return reply, newSessionID
}

func findLatestSessionID() string {
	files, err := os.ReadDir(chatsDir)
	if err != nil {
		log.Printf("Error reading chats directory: %v", err)
		return ""
	}

	var latestFile os.DirEntry
	var latestTime time.Time

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") || !strings.HasPrefix(file.Name(), "session-") {
			continue
		}
		info, err := file.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = file
		}
	}

	if latestFile != nil {
		name := latestFile.Name()
		name = strings.TrimSuffix(name, ".json")
		parts := strings.Split(name, "-")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	return ""
}

func handleVoiceMessage(message *tgbotapi.Message) {
	userState := getUserState(message.From.ID)
	userState.IsProcessing = true
	defer func() { userState.IsProcessing = false }()

	// Check if we have an API key
	if geminiAPIKey == "" {
		msg := tgbotapi.NewMessage(message.Chat.ID,
			"🎤 Voice transcription requires a Gemini API key.\n\n"+
				"Please visit https://aistudio.google.com/apikey to generate a key, "+
				"then paste it here. Your key will be saved securely.\n\n"+
				"Or type 'cancel' to continue without voice transcription.")
		userState := getUserState(message.From.ID)
		userState.State = "waiting_api_key"
		bot.Send(msg)
		return
	}

	// Check voice message duration
	if message.Voice.Duration > maxVoiceDurationSeconds {
		minutes := maxVoiceDurationSeconds / 60
		msg := tgbotapi.NewMessage(message.Chat.ID,
			fmt.Sprintf("❌ Voice message too long. Maximum duration is %d minutes (%d seconds).\n"+
				"Your message is %d seconds long.", minutes, maxVoiceDurationSeconds, message.Voice.Duration))
		bot.Send(msg)
		return
	}

	// Check file size
	if message.Voice.FileSize > maxVoiceFileSizeBytes {
		sizeMB := maxVoiceFileSizeBytes / (1024 * 1024)
		msg := tgbotapi.NewMessage(message.Chat.ID,
			fmt.Sprintf("❌ Voice message file too large. Maximum size is %dMB.", sizeMB))
		bot.Send(msg)
		return
	}

	// Start typing indicator
	stopTyping := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			bot.Send(tgbotapi.NewChatAction(message.Chat.ID, tgbotapi.ChatTyping))
			select {
			case <-ticker.C:
			case <-stopTyping:
				return
			}
		}
	}()
	defer close(stopTyping)

	text, err := transcribeVoice(message.Voice.FileID)
	if err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("❌ Error transcribing voice: %v", err))
		bot.Send(msg)
		return
	}

	// If transcription is empty or very short, inform user
	if strings.TrimSpace(text) == "" {
		msg := tgbotapi.NewMessage(message.Chat.ID, "🎤 No speech detected in voice message.")
		bot.Send(msg)
		return
	}

	context := ""
	if message.ReplyToMessage != nil {
		context = fmt.Sprintf("Context: %s: %s\n\n",
			message.ReplyToMessage.From.FirstName,
			message.ReplyToMessage.Text)
	}
	prompt := fmt.Sprintf("%sYou are an assistant in a Telegram chat.\nAnswer this voice message (transcribed):\n\n%s: %s",
		context, message.From.FirstName, text)

	reply, newSessionID := callGemini(prompt, userState.LastSessionID)
	if newSessionID != "" {
		userState.LastSessionID = newSessionID
	}

	msg := tgbotapi.NewMessage(message.Chat.ID, reply)
	if message.ReplyToMessage != nil {
		msg.ReplyToMessageID = message.MessageID
	}
	bot.Send(msg)
}

func handleAPIKeyInput(message *tgbotapi.Message) {
	text := strings.TrimSpace(message.Text)
	userState := getUserState(message.From.ID)

	if strings.ToLower(text) == "cancel" {
		userState.State = ""
		msg := tgbotapi.NewMessage(message.Chat.ID, "✅ Cancelled. You can continue chatting without voice transcription.")
		bot.Send(msg)
		return
	}

	// Validate API key format (basic validation)
	if !strings.HasPrefix(text, "AIza") || len(text) < 30 {
		msg := tgbotapi.NewMessage(message.Chat.ID,
			"❌ Invalid API key format. Gemini API keys typically start with 'AIza' and are longer.\n"+
				"Please check your key and try again, or type 'cancel' to skip.")
		bot.Send(msg)
		return
	}

	// Test the API key with a simple request
	if !testAPIKey(text) {
		msg := tgbotapi.NewMessage(message.Chat.ID,
			"❌ API key test failed. Please check that your key is valid .\n"+
				"Try again or type 'cancel' to skip.")
		bot.Send(msg)
		return
	}

	// Save API key to environment file
	if err := saveAPIKeyToEnv(text); err != nil {
		log.Printf("Error saving API key to .env: %v", err)
		msg := tgbotapi.NewMessage(message.Chat.ID,
			"⚠️ API key validated but failed to save to file. It will work for this session only.\n"+
				fmt.Sprintf("Error: %v", err))
		bot.Send(msg)
	} else {
		log.Printf("API key successfully saved to .env file")
	}

	// Update global API key and clear user state
	geminiAPIKey = text
	userState.State = ""

	msg := tgbotapi.NewMessage(message.Chat.ID, "✅ API key validated and saved! You can now send voice messages for transcription.")
	bot.Send(msg)

	// Delete the message containing the API key for security
	deleteMsg := tgbotapi.NewDeleteMessage(message.Chat.ID, message.MessageID)
	if _, err := bot.Request(deleteMsg); err != nil {
		log.Printf("Warning: Could not delete API key message: %v", err)
	}
}

func testAPIKey(apiKey string) bool {
	// Simple test with a minimal request
	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": "Hello"},
				},
			},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return false
	}

	req, err := http.NewRequest("POST",
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key="+apiKey,
		bytes.NewBuffer(jsonData))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

func saveAPIKeyToEnv(apiKey string) error {
	// Read existing .env file content
	envContent := make(map[string]string)

	if data, err := os.ReadFile(envFilePath); err == nil {
		// Parse existing .env file
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				envContent[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}

	// Update API key
	envContent["GEMINI_API_KEY"] = apiKey

	// Write back to file
	var envLines []string

	// Keep existing variables in order, update if exists
	if data, err := os.ReadFile(envFilePath); err == nil {
		lines := strings.Split(string(data), "\n")
		apiKeyWritten := false

		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				envLines = append(envLines, line)
				continue
			}

			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				if key == "GEMINI_API_KEY" {
					envLines = append(envLines, fmt.Sprintf("GEMINI_API_KEY=%s", apiKey))
					apiKeyWritten = true
				} else {
					envLines = append(envLines, line)
				}
			} else {
				envLines = append(envLines, line)
			}
		}

		// If API key wasn't in file, add it
		if !apiKeyWritten {
			envLines = append(envLines, fmt.Sprintf("GEMINI_API_KEY=%s", apiKey))
		}
	} else {
		// File doesn't exist, create new content
		envLines = append(envLines, fmt.Sprintf("GEMINI_API_KEY=%s", apiKey))
	}

	// Write to file
	return os.WriteFile(envFilePath, []byte(strings.Join(envLines, "\n")), 0o600)
}

func transcribeVoice(fileID string) (string, error) {
	file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}

	fileURL := file.Link(bot.Token)
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download file: status %d", resp.StatusCode)
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read audio data: %w", err)
	}

	// Determine MIME type based on file extension or content
	mimeType := "audio/ogg" // Default for Telegram voice messages
	if strings.Contains(file.FilePath, ".mp3") {
		mimeType = "audio/mpeg"
	} else if strings.Contains(file.FilePath, ".wav") {
		mimeType = "audio/wav"
	} else if strings.Contains(file.FilePath, ".m4a") {
		mimeType = "audio/mp4"
	}

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{
						"text": "Please transcribe this audio file accurately. Only return the transcribed text without any additional commentary.",
					},
					{
						"inline_data": map[string]interface{}{
							"mime_type": mimeType,
							"data":      base64.StdEncoding.EncodeToString(audioData),
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST",
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key="+geminiAPIKey,
		bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Hour}
	apiResp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer apiResp.Body.Close()

	if apiResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(apiResp.Body)
		return "", fmt.Errorf("API returned status %d: %s", apiResp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(apiResp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Error.Code != 0 {
		return "", fmt.Errorf("API error %d: %s", result.Error.Code, result.Error.Message)
	}

	if len(result.Candidates) > 0 && len(result.Candidates[0].Content.Parts) > 0 {
		return strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text), nil
	}

	return "", fmt.Errorf("no transcription received from API")
}
