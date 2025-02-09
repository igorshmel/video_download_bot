package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gopkg.in/yaml.v3"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config структура для конфигурации
type Config struct {
	Telegram struct {
		Token string `yaml:"token"`
	} `yaml:"telegram"`
}

func main() {
	// Загрузка конфигурации
	config, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	// Создание бота
	bot, err := tgbotapi.NewBotAPI(config.Telegram.Token)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	bot.Debug = true
	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		msg := handleMessage(update.Message, bot)
		if msg != nil {
			if _, err := bot.Send(*msg); err != nil {
				log.Printf("Failed to send message: %v", err)
			}
		}
	}
}

// LoadConfig загружает конфигурацию из YAML-файла
func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			log.Printf("Error closing config file: %v", cerr)
		}
	}()

	var config Config
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	return &config, nil
}

// generateUUID генерирует уникальный идентификатор
func generateUUID() (string, error) {
	u := make([]byte, 16)
	if _, err := rand.Read(u); err != nil {
		return "", err
	}
	// Форматируем UUID
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:]), nil
}

func DownloadVideoWithYTDLP(ctx context.Context, url string) (string, error) {
	// Генерируем уникальное имя (без расширения)
	uuid, err := generateUUID()
	if err != nil {
		return "", fmt.Errorf("failed to generate UUID: %w", err)
	}
	outputTemplate := fmt.Sprintf("downloads/%s.%%(ext)s", uuid) // Используем {ext} для сохранения оригинального расширения

	cmd := exec.CommandContext(ctx, "yt-dlp", "-o", outputTemplate, url)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("yt-dlp failed: %w\nOutput: %s", err, string(output))
	}

	log.Printf("yt-dlp output: %s", string(output))

	// Поиск загруженного файла
	dir := "downloads"
	files, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read downloads directory: %w", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), uuid) {
			return filepath.Join(dir, file.Name()), nil
		}
	}

	return "", fmt.Errorf("failed to find downloaded file with UUID: %s", uuid)
}

func handleMessage(msg *tgbotapi.Message, bot *tgbotapi.BotAPI) *tgbotapi.MessageConfig {
	chatID := msg.Chat.ID
	switch msg.Command() {
	case "start":
		return createMessage(chatID, "Welcome! I am your bot.")
	case "help":
		helpText := "Commands:\n/start - Start the bot\n/help - Show this message\n/vid - Upload a video"
		return createMessage(chatID, helpText)
	default:
		return downloadAndProcessVideo(chatID, msg.Text, bot)
	}
}
func downloadAndProcessVideo(chatID int64, videoURL string, bot *tgbotapi.BotAPI) *tgbotapi.MessageConfig {
	go func(chatID int64, videoURL string, bot *tgbotapi.BotAPI) { // Передача параметров в горутину
		statusMsg := createMessage(chatID, "Downloading video...")
		if _, err := bot.Send(*statusMsg); err != nil {
			log.Printf("Failed to send status message: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		downloadedFile, err := DownloadVideoWithYTDLP(ctx, videoURL)
		if err != nil {
			log.Printf("Error downloading video: %v", err)
			if _, sendErr := bot.Send(*createMessage(chatID, "Error downloading video")); sendErr != nil {
				log.Printf("Failed to send error message: %v", sendErr)
			}
			return
		}

		// Проверка размера файла
		fileInfo, err := os.Stat(downloadedFile)
		if err != nil {
			log.Printf("Failed to get file info: %v", err)
			if _, sendErr := bot.Send(*createMessage(chatID, "Error checking file size")); sendErr != nil {
				log.Printf("Failed to send error message: %v", sendErr)
			}
			return
		}

		const maxSize int64 = 50 * 1024 * 1024 // 50MB
		if fileInfo.Size() > maxSize {
			log.Printf("File %s is too large (%.2f MB)", downloadedFile, float64(fileInfo.Size())/1024/1024)
			if _, sendErr := bot.Send(*createMessage(chatID, fmt.Sprintf("File downloaded: %s\nBut it's too large to send (%.2f MB)", downloadedFile, float64(fileInfo.Size())/1024/1024))); sendErr != nil {
				log.Printf("Failed to send file too large message: %v", sendErr)
			}
			return
		}

		// Отправляем файл
		if _, err := bot.Send(tgbotapi.NewVideo(chatID, tgbotapi.FilePath(downloadedFile))); err != nil {
			log.Printf("Error sending video: %v", err)
			if _, sendErr := bot.Send(*createMessage(chatID, "Failed to send video")); sendErr != nil {
				log.Printf("Failed to send failure message: %v", sendErr)
			}
			return
		}

		// Удаляем файл после успешной отправки
		if err := os.Remove(downloadedFile); err != nil {
			log.Printf("Error deleting file %s: %v", downloadedFile, err)
		}
	}(chatID, videoURL, bot) // Вызов анонимной функции с параметрами

	return createMessage(chatID, "Video processing started...")
}
func createMessage(chatID int64, text string) *tgbotapi.MessageConfig {
	m := tgbotapi.NewMessage(chatID, text)
	return &m
}

func sendRawVideo(chatID int64, filePath string, bot *tgbotapi.BotAPI) *tgbotapi.MessageConfig {
	video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	if _, err := bot.Send(video); err != nil {
		log.Printf("Error sending video: %v", err)
		return createMessage(chatID, "Failed to send video")
	}
	return createMessage(chatID, "Video sent successfully!")
}
