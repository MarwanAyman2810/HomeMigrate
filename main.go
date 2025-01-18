package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"

	"fyne.io/fyne/v2/widget"
	"github.com/shirou/gopsutil/disk"
)

func init() {
	// Set up logging to a file
	logFile, err := os.OpenFile("debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		return
	}
	log.SetOutput(logFile)
}

func main() {
	fmt.Println("Starting application...")

	// Set environment variables for Cinnamon
	os.Setenv("XDG_CURRENT_DESKTOP", "X-Cinnamon")
	os.Setenv("XDG_SESSION_DESKTOP", "cinnamon")
	os.Setenv("DESKTOP_SESSION", "cinnamon")

	// Force X11 driver
	os.Setenv("FYNE_DRIVER", "x11")

	myApp := app.New()
	fmt.Println("Created new Fyne app")

	window := myApp.NewWindow("Home Folder Migration")
	fmt.Println("Created new window")

	// Status label to show current operation
	status := widget.NewLabel("Waiting for USB drive...")
	progress := widget.NewProgressBar()
	progress.Hide()

	// Create a channel to receive USB detection events
	usbChan := make(chan string)

	// Start USB detection in background
	go detectUSB(usbChan)
	fmt.Println("Started USB detection")

	// Button to start migration
	startBtn := widget.NewButton("Start Migration", func() {
		fmt.Println("Start button clicked")
		usbPath := <-usbChan
		if usbPath == "" {
			dialog.ShowError(fmt.Errorf("no USB drive detected"), window)
			return
		}

		progress.Show()
		go func() {
			err := copyHomeFolder(usbPath, progress)
			if err != nil {
				dialog.ShowError(err, window)
				status.SetText("Migration failed: " + err.Error())
			} else {
				dialog.ShowInformation("Success", "Home folder migration completed!", window)
				status.SetText("Migration completed successfully!")
			}
			progress.Hide()
		}()
	})

	// Layout
	content := container.NewVBox(
		status,
		progress,
		startBtn,
	)

	window.SetContent(content)
	window.Resize(fyne.NewSize(300, 200))
	fmt.Println("Set up window content")

	fmt.Println("About to show window")
	window.ShowAndRun()
	fmt.Println("Window closed") // This will only print when the window is closed
}

func detectUSB(usbChan chan string) {
	var previousPartitions []string

	for {
		partitions, err := disk.Partitions(false)
		if err != nil {
			log.Println("Error getting disk partitions:", err)
			continue
		}

		currentPartitions := make([]string, 0)
		for _, partition := range partitions {
			if strings.HasPrefix(partition.Device, "/dev/sd") {
				currentPartitions = append(currentPartitions, partition.Mountpoint)
			}
		}

		// Check for new USB drives
		for _, current := range currentPartitions {
			found := false
			for _, previous := range previousPartitions {
				if current == previous {
					found = true
					break
				}
			}
			if !found {
				// New USB drive detected
				log.Println("New USB drive detected:", current)
				usbChan <- current
			}
		}

		previousPartitions = currentPartitions
		time.Sleep(2 * time.Second)
	}
}

func copyHomeFolder(destPath string, progress *widget.ProgressBar) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Println("Error getting home directory:", err)
		return fmt.Errorf("error getting home directory: %v", err)
	}

	// Count total files for progress bar
	var totalFiles int64
	filepath.Walk(homeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Println("Error walking home directory:", err)
			return nil
		}
		if !info.IsDir() {
			totalFiles++
		}
		return nil
	})

	var copiedFiles int64
	progress.SetValue(0)

	// Create the destination folder
	destHomeDir := filepath.Join(destPath, "home_backup")
	err = os.MkdirAll(destHomeDir, 0755)
	if err != nil {
		log.Println("Error creating destination directory:", err)
		return fmt.Errorf("error creating destination directory: %v", err)
	}

	// Copy files
	err = filepath.Walk(homeDir, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			log.Println("Error walking home directory:", err)
			return err
		}

		// Calculate relative path
		relPath, err := filepath.Rel(homeDir, srcPath)
		if err != nil {
			log.Println("Error calculating relative path:", err)
			return err
		}

		destFilePath := filepath.Join(destHomeDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(destFilePath, info.Mode())
		}

		// Copy the file
		err = copyFile(srcPath, destFilePath)
		if err != nil {
			log.Println("Error copying file:", err)
			return err
		}

		copiedFiles++
		progress.SetValue(float64(copiedFiles) / float64(totalFiles))
		return nil
	})

	if err != nil {
		log.Println("Error copying files:", err)
		return fmt.Errorf("error copying files: %v", err)
	}

	return nil
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		log.Println("Error opening source file:", err)
		return err
	}
	defer sourceFile.Close()

	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		log.Println("Error creating destination directory:", err)
		return err
	}

	destFile, err := os.Create(dst)
	if err != nil {
		log.Println("Error creating destination file:", err)
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		log.Println("Error copying file:", err)
		return err
	}

	// Preserve file permissions
	sourceInfo, err := os.Stat(src)
	if err != nil {
		log.Println("Error getting source file info:", err)
		return err
	}

	return os.Chmod(dst, sourceInfo.Mode())
}
