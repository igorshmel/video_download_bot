package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/davesavic/clink"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gopkg.in/yaml.v3"
)

// Config структура для конфигурации
type Config struct {
	Telegram struct {
		Token string `yaml:"token"`
	} `yaml:"telegram"`
	Yandex struct {
		Token string `yaml:"token"`
	} `yaml:"yandex"`
}

const RemotePath = "test"

func main() {
	// Загрузка конфигурации
	config, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	startCleanupScheduler()

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
func DownloadMediaWithYTDLP(ctx context.Context, url string, audioOnly bool, clipRange string, chatID int64, bot *tgbotapi.BotAPI) (string, error) {
	uuid, err := generateUUID()
	if err != nil {
		return "", fmt.Errorf("failed to generate UUID: %w", err)
	}
	outputTemplate := fmt.Sprintf("downloads/%s.%%(ext)s", uuid)

	var cmd *exec.Cmd
	if audioOnly {
		cmd = exec.CommandContext(ctx, "yt-dlp", "-f", "bestaudio", "--extract-audio", "--audio-format", "mp3", "--audio-quality", "0", "-o", outputTemplate, url)
	} else if clipRange != "" {
		cmd = exec.CommandContext(ctx, "yt-dlp", "--merge-output-format", "mp4", "-o", outputTemplate, "--download-sections", "*"+clipRange, url)
	} else {
		cmd = exec.CommandContext(ctx, "yt-dlp", "--progress-template", "%(progress._percent_str)s", "--merge-output-format", "mp4", "-o", outputTemplate, url)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(stdout)
	go func() {
		for scanner.Scan() {
			progress := scanner.Text()
			fmt.Printf("stdout: %s\n", progress)
			if strings.Contains(progress, "25.0%") {
				if _, err := bot.Send(*createMessage(chatID, "25% downloaded...")); err != nil {
					log.Printf("Failed to send progress message: %v", err)
				}
			} else if strings.Contains(progress, "50.0%") {
				if _, err := bot.Send(*createMessage(chatID, "50% downloaded...")); err != nil {
					log.Printf("Failed to send progress message: %v", err)
				}
			} else if strings.Contains(progress, "75.0%") {
				if _, err := bot.Send(*createMessage(chatID, "75% downloaded...")); err != nil {
					log.Printf("Failed to send progress message: %v", err)
				}
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		return "", err
	}

	if err := cmd.Wait(); err != nil {
		return "", err
	}

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

// downloadMedia downloads a video or audio file from the given URL using yt-dlp
// It returns the path to the downloaded file and any error encountered
// The function supports downloading audio-only files and specific video clips
// Progress updates are sent to the Telegram chat during download

func handleMessage(msg *tgbotapi.Message, bot *tgbotapi.BotAPI) *tgbotapi.MessageConfig {
	chatID := msg.Chat.ID
	args := strings.Fields(msg.Text)

	if msg.Command() == "" && len(args) == 1 && strings.HasPrefix(args[0], "http") {
		return downloadAndProcessMedia(chatID, args[0], bot, false, "")
	}

	if len(args) < 2 {
		return createMessage(chatID, "Invalid command format. Use: /command URL [time_range]")
	}

	uRL := args[1]
	clipRange := ""
	if len(args) == 3 {
		clipRange = args[2]
	}

	switch msg.Command() {
	case "start":
		return createMessage(chatID, "Welcome! I am your bot.")
	case "help":
		return createMessage(chatID, "Commands:\n/start - Start the bot\n/help - Show this message\n/vid - Download and send a video\n/audio - Download and send an audio file\n/clip - Download a video clip with a specified time range\n/cleanup - Delete all downloaded files")
	case "audio":
		return downloadAndProcessMedia(chatID, uRL, bot, true, "")
	case "clip":
		return downloadAndProcessMedia(chatID, uRL, bot, false, clipRange)
	case "cleanup":
		if err := cleanupDownloads(); err != nil {
			return createMessage(chatID, "Error cleaning up downloads folder")
		}
		return createMessage(chatID, "All downloaded files have been deleted.")
	default:
		return downloadAndProcessMedia(chatID, uRL, bot, false, "")
	}
}

func uploadToYandexDisk(filePath, fileName string) (string, error) {
	targetURL, err := getYandexDiskInfo(RemotePath, fileName)
	if err != nil {
		return "", fmt.Errorf("failed to get upload URL: %w", err)
	}

	YandexPut(targetURL, filepath.Dir(filePath), fileName)

	shareLink, err := getYandexDiskShareLink(RemotePath, fileName)
	if err != nil {
		return "", fmt.Errorf("failed to get share link: %w", err)
	}
	return shareLink, nil
}

func downloadAndProcessMedia(chatID int64, mediaURL string, bot *tgbotapi.BotAPI, audioOnly bool, clipRange string) *tgbotapi.MessageConfig {
	go func(chatID int64, mediaURL string, bot *tgbotapi.BotAPI, audioOnly bool, clipRange string) {
		statusMsg := createMessage(chatID, "Downloading media...")
		if _, err := bot.Send(*statusMsg); err != nil {
			log.Printf("Failed to send status message: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		downloadedFile, err := DownloadMediaWithYTDLP(ctx, mediaURL, audioOnly, clipRange, chatID, bot)
		if err != nil {
			log.Printf("Error downloading media: %v", err)
			if _, err := bot.Send(*createMessage(chatID, "Error downloading media")); err != nil {
				log.Printf("Failed to send error message: %v", err)
			}
			return
		}

		fileInfo, err := os.Stat(downloadedFile)
		if err != nil {
			log.Printf("Failed to get file info: %v", err)
			if _, err := bot.Send(*createMessage(chatID, "Error checking file size")); err != nil {
				log.Printf("Failed to send error message: %v", err)
			}
			return
		}

		const maxSize int64 = 50 * 1024 * 1024 // 50MB
		if fileInfo.Size() > maxSize {
			if _, err := bot.Send(*createMessage(chatID, "File is too large, uploading to Yandex Disk...")); err != nil {
				log.Printf("Failed to send status message: %v", err)
			}

			uploadURL, err := uploadToYandexDisk(downloadedFile, fileInfo.Name())
			if err != nil {
				log.Printf("Failed to upload file to Yandex Disk: %v", err)
				if _, err := bot.Send(*createMessage(chatID, "Error uploading large file to Yandex Disk")); err != nil {
					log.Printf("Failed to send error message: %v", err)
				}
				return
			}

			if _, err := bot.Send(*createMessage(chatID, fmt.Sprintf("File uploaded to Yandex Disk: %s", uploadURL))); err != nil {
				log.Printf("Failed to send success message: %v", err)
			}

			if err := os.Remove(downloadedFile); err != nil {
				log.Printf("Error deleting file %s: %v", downloadedFile, err)
			}
			return
		}

		var mediaMsg tgbotapi.Chattable
		if audioOnly {
			mediaMsg = tgbotapi.NewAudio(chatID, tgbotapi.FilePath(downloadedFile))
		} else {
			mediaMsg = tgbotapi.NewVideo(chatID, tgbotapi.FilePath(downloadedFile))
		}

		if _, err := bot.Send(mediaMsg); err != nil {
			log.Printf("Error sending media: %v", err)
			if _, err := bot.Send(*createMessage(chatID, "Failed to send media")); err != nil {
				log.Printf("Failed to send error message: %v", err)
			}
			return
		}

		if err := os.Remove(downloadedFile); err != nil {
			log.Printf("Error deleting file %s: %v", downloadedFile, err)
		}
	}(chatID, mediaURL, bot, audioOnly, clipRange)

	return createMessage(chatID, "Media processing started...")
}

func createMessage(chatID int64, text string) *tgbotapi.MessageConfig {
	m := tgbotapi.NewMessage(chatID, text)
	return &m
}

// getYandexDiskInfo --
func getYandexDiskInfo(remotePath string, fileName string) (string, error) {
	// Загрузка конфигурации
	config, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	path := url.PathEscape(fmt.Sprintf("%s/%s", remotePath, fileName))

	// Create a new client with default options.
	client := clink.NewClient()
	urlString := fmt.Sprintf("%s%s%s%s%s", "https://cloud-api.yandex.net/v1/disk/resources/upload", "?", "path=", path, "&overwrite=true")

	headers := map[string]string{
		"Authorization": "OAuth " + config.Yandex.Token,
	}
	client.Headers = headers
	// Create a new request with default options.
	req, err := http.NewRequest(http.MethodGet, urlString, nil)
	if err != nil {
		return "", err
	}

	// Send the request and get the response.
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	// Hydrate the response body into a map.
	var response map[string]any
	if err = clink.ResponseToJson(resp, &response); err != nil {
		return "", err
	}

	// Check if "href" key exists in the map.
	targetUrl, ok := response["href"].(string)
	if !ok {
		return "", fmt.Errorf("href is not a string")
	}

	// Print the target map.
	return targetUrl, nil
}

func getYandexDiskShareLink(remotePath string, fileName string) (string, error) {
	// Загрузка конфигурации
	config, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	path := url.PathEscape(fmt.Sprintf("%s/%s", remotePath, fileName))

	// Create a new client with default options.
	client := clink.NewClient()
	urlString := fmt.Sprintf("%s%s%s%s", "https://cloud-api.yandex.net/v1/disk/resources/download", "?", "path=", path)

	headers := map[string]string{
		"Authorization": "OAuth " + config.Yandex.Token,
		"Accept":        "application/json",
	}
	client.Headers = headers

	// Create a new request with default options.
	req, err := http.NewRequest(http.MethodGet, urlString, nil)
	if err != nil {
		return "", err
	}

	// Send the request and get the response.
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	// Hydrate the response body into a map.
	var response map[string]any
	if err = clink.ResponseToJson(resp, &response); err != nil {
		return "", err
	}

	// Check if "href" key exists in the map.
	targetUrl, ok := response["href"].(string)
	if !ok {
		return "", fmt.Errorf("href is not a string")
	}

	return targetUrl, nil
}

// YandexPut --
func YandexPut(targetUrl string, localPath string, fileName string) {
	// Загрузка конфигурации
	config, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}
	req, err := http.NewRequest("PUT", targetUrl, nil)
	if err != nil {
		fmt.Println("Error creating HTTP request:", err)
		return
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "OAuth "+config.Yandex.Token)

	f, err := os.Open(fmt.Sprintf("%s/%s", localPath, fileName))
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer func(f *os.File) {
		err = f.Close()
		if err != nil {

		}
	}(f)
	fmt.Println("FileName:", f.Name())
	req.Body = f

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error making HTTP request:", err)
		return
	}
	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {

		}
	}(resp.Body)

	fmt.Println("HTTP response status:", resp.Status)
}

func cleanupDownloads() error {
	downloadDir := "downloads"
	files, err := os.ReadDir(downloadDir)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, file := range files {
		filePath := filepath.Join(downloadDir, file.Name())
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			log.Printf("Failed to get file info for %s: %v", filePath, err)
			continue
		}
		if fileInfo.ModTime().Before(cutoff) {
			if err := os.Remove(filePath); err != nil {
				log.Printf("Failed to delete file %s: %v", filePath, err)
			}
		}
	}
	return nil
}

func startCleanupScheduler() {
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			err := cleanupDownloads()
			if err != nil {
			}
		}
	}()
}
