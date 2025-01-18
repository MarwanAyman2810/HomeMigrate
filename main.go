package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// "unicode"
	// "unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/shirou/gopsutil/disk"
	"golang.org/x/sys/unix"
)

func init() {
	// Set up logging to a file
	logFile, err := os.OpenFile("debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		return
	}
	log.SetOutput(io.MultiWriter(logFile, os.Stdout))
}

// Custom logger that also updates the UI
type uiLogger struct {
	textArea *widget.Entry
	mu       sync.Mutex
}

func (l *uiLogger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Update the text area
	if l.textArea != nil {
		l.textArea.SetText(l.textArea.Text + string(p))
		l.textArea.Refresh()
	}

	return len(p), nil
}

type USBEvent struct {
	path    string
	removed bool
}

type USBDrive struct {
	name string
	path string
	size int64 // size in bytes
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
	window.Resize(fyne.NewSize(600, 400)) // Set a fixed size of 600x400
	window.SetFixedSize(true)             // Prevent window resizing
	fmt.Println("Created new window")

	// Status label to show current operation
	status := widget.NewLabel("Waiting for USB drive...")
	progress := widget.NewProgressBar()
	progress.Hide()

	// Create log text area
	logArea := widget.NewMultiLineEntry()
	logArea.Disable() // Make it read-only
	logArea.Resize(fyne.NewSize(580, 150))

	// Set up custom logger
	uiLog := &uiLogger{textArea: logArea}
	log.SetOutput(io.MultiWriter(uiLog, os.Stdout))

	// Create a channel to receive USB detection events
	usbChan := make(chan USBEvent)
	var availableUSBs []USBDrive

	// Create a dropdown to show available USB drives
	usbSelect := widget.NewSelect([]string{}, func(selected string) {
		if selected != "" {
			status.SetText(fmt.Sprintf("Selected USB drive: %s\nClick Start Migration to begin", selected))
		}
	})
	usbSelect.PlaceHolder = "Select USB Drive"
	usbSelect.Hide()

	// Start USB detection in background
	go detectUSB(usbChan)
	fmt.Println("Started USB detection")

	// Start a goroutine to handle USB events
	go func() {
		for event := range usbChan {
			if event.removed {
				// Remove the USB from available list
				for i, usb := range availableUSBs {
					if usb.path == event.path {
						availableUSBs = append(availableUSBs[:i], availableUSBs[i+1:]...)
						break
					}
				}
			} else if event.path != "" {
				// Add new USB to available list
				name := filepath.Base(event.path)
				// Get drive size
				var stat unix.Statfs_t
				if err := unix.Statfs(event.path, &stat); err == nil {
					totalSize := int64(stat.Blocks) * int64(stat.Bsize)
					availableUSBs = append(availableUSBs, USBDrive{
						name: name,
						path: event.path,
						size: totalSize,
					})
				} else {
					log.Printf("Error getting size for %s: %v", event.path, err)
					availableUSBs = append(availableUSBs, USBDrive{
						name: name,
						path: event.path,
						size: 0,
					})
				}
			}

			// Update the dropdown options
			var names []string
			for _, usb := range availableUSBs {
				sizeGB := float64(usb.size) / (1024 * 1024 * 1024)
				names = append(names, fmt.Sprintf("%s (%.1f GB)", usb.name, sizeGB))
			}
			usbSelect.Options = names

			// If the currently selected USB was removed, clear the selection
			if event.removed {
				selectedName := usbSelect.Selected
				found := false
				for _, usb := range availableUSBs {
					if usb.name == selectedName {
						found = true
						break
					}
				}
				if !found {
					usbSelect.Selected = ""
					usbSelect.Refresh()
				}
			}

			// Update visibility and status
			if len(availableUSBs) > 0 {
				status.SetText("Please select a USB drive for migration")
				usbSelect.Show()
			} else {
				status.SetText("Waiting for USB drive...")
				usbSelect.Hide()
				usbSelect.Selected = ""
			}
			usbSelect.Refresh()
		}
	}()

	// Button to start migration
	startBtn := widget.NewButton("Start Migration", func() {
		fmt.Println("Start button clicked")
		if usbSelect.Selected == "" {
			dialog.ShowError(fmt.Errorf("please select a USB drive first"), window)
			return
		}

		// Find the full path for the selected name
		var selectedPath string
		for _, usb := range availableUSBs {
			if usb.name == strings.Split(usbSelect.Selected, " ")[0] {
				selectedPath = usb.path
				break
			}
		}

		progress.Show()
		go func() {
			err := copyHomeFolder(selectedPath, progress)
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

	// Create main container with padding
	content := container.NewVBox(
		status,
		usbSelect,
		startBtn,
		progress,
		widget.NewLabel("Logs:"),
		logArea,
	)

	window.SetContent(content)
	fmt.Println("Set up window content")

	fmt.Println("About to show window")
	window.ShowAndRun()
	fmt.Println("Window closed")
}

func detectUSB(usbChan chan USBEvent) {
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
				usbChan <- USBEvent{path: current, removed: false}
			}
		}

		// Check for removed USB drives
		for _, previous := range previousPartitions {
			found := false
			for _, current := range currentPartitions {
				if current == previous {
					found = true
					break
				}
			}
			if !found {
				// USB drive removed
				log.Println("USB drive removed:", previous)
				usbChan <- USBEvent{path: previous, removed: true}
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

	// Create the destination folder with absolute path
	destHomeDir := filepath.Join(destPath, "home_backup")
	absDestHomeDir, err := filepath.Abs(destHomeDir)
	if err != nil {
		log.Println("Error getting absolute path:", err)
		return fmt.Errorf("error getting absolute path: %v", err)
	}

	// Count total files for progress bar
	var totalFiles int64
	filepath.Walk(homeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Println("Error walking home directory:", err)
			return nil
		}
		
		// Skip hidden files and directories
		if strings.HasPrefix(filepath.Base(path), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		
		// Skip system and cache directories
		if strings.Contains(path, "go/pkg/mod") || 
		   strings.Contains(path, ".cache") || 
		   strings.Contains(path, ".local/share") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		
		// Skip the backup directory
		absPath, _ := filepath.Abs(path)
		if strings.HasPrefix(absPath, absDestHomeDir) {
			if info.IsDir() {
				return filepath.SkipDir
			}
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

		// Skip hidden files and directories
		if strings.HasPrefix(filepath.Base(srcPath), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip system and cache directories
		if strings.Contains(srcPath, "go/pkg/mod") || 
		   strings.Contains(srcPath, ".cache") || 
		   strings.Contains(srcPath, ".local/share") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip the backup directory using absolute path comparison
		absSrcPath, _ := filepath.Abs(srcPath)
		if strings.HasPrefix(absSrcPath, absDestHomeDir) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Calculate relative path
		relPath, err := filepath.Rel(homeDir, srcPath)
		if err != nil {
			log.Println("Error calculating relative path:", err)
			return err
		}

		destFilePath := filepath.Join(destHomeDir, relPath)

		if info.IsDir() {
			err := os.MkdirAll(destFilePath, info.Mode())
			if err != nil {
				log.Printf("Error creating directory %s: %v", destFilePath, err)
				return err
			}
			return nil // Skip further processing for directories
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
