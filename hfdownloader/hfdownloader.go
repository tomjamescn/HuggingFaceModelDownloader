package hfdownloader

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

const (
	AgreementModelURL      = "https://huggingface.co/%s"
	AgreementDatasetURL    = "https://huggingface.co/datasets/%s"
	RawModelFileURL        = "https://huggingface.co/%s/raw/%s/%s"
	RawDatasetFileURL      = "https://huggingface.co/datasets/%s/raw/%s/%s"
	LfsModelResolverURL    = "https://huggingface.co/%s/resolve/%s/%s"
	LfsDatasetResolverURL  = "https://huggingface.co/datasets/%s/resolve/%s/%s"
	JsonModelsFileTreeURL  = "https://huggingface.co/api/models/%s/tree/%s/%s"
	JsonDatasetFileTreeURL = "https://huggingface.co/api/datasets/%s/tree/%s/%s"
)

// I may use this coloring thing later on
var (
	infoColor      = color.New(color.FgGreen).SprintFunc()
	warningColor   = color.New(color.FgYellow).SprintFunc()
	errorColor     = color.New(color.FgRed).SprintFunc()
	NumConnections = 5
	RequiresAuth   = false
	AuthToken      = ""
)

type hfmodel struct {
	Type        string `json:"type"`
	Oid         string `json:"oid"`
	Size        int    `json:"size"`
	Path        string `json:"path"`
	IsDirectory bool
	IsLFS       bool

	AppendedPath    string
	SkipDownloading bool
	FilterSkip      bool
	DownloadLink    string
	Lfs             *hflfs `json:"lfs,omitempty"`
}

type hflfs struct {
	Oid_SHA265  string `json:"oid"` // in lfs, oid is sha256 of the file
	Size        int64  `json:"size"`
	PointerSize int    `json:"pointerSize"`
}

func DownloadModel(ModelDatasetName string, AppendFilterToPath bool, SkipSHA bool, IsDataset bool, DestintionBasePath string, ModelBranch string, concurrentConnctionions int, token string) error {
	NumConnections = concurrentConnctionions

	//make sure we dont include dataset filter within folder creation
	modelP := ModelDatasetName
	HasFilter := false
	if strings.Contains(modelP, ":") {
		modelP = strings.Split(ModelDatasetName, ":")[0]
		HasFilter = true
	}
	//modelPath := path.Join(DestintionBasePath, strings.Replace(modelP, "/", "_", -1))
	modelPath := path.Join(DestintionBasePath, modelP)
	if token != "" {
		RequiresAuth = true
		AuthToken = token
	}

	if HasFilter && AppendFilterToPath { //for this feature, I'll just simple re-run the script and apply one filter at a time
		filters := strings.Split(strings.Split(ModelDatasetName, ":")[1], ",")
		for _, ff := range filters {
			//create folders

			ffpath := fmt.Sprintf("%s_f_%s", modelPath, ff)
			err := os.MkdirAll(ffpath, os.ModePerm)
			if err != nil {
				// fmt.Println("Error:", err)
				return err
			}
			newModelDatasetName := fmt.Sprintf("%s:%s", modelP, ff)
			err = processHFFolderTree(ffpath, IsDataset, SkipSHA, newModelDatasetName, ModelBranch, "") // passing empty as foldername, because its the first root folder
			if err != nil {
				// fmt.Println("Error:", err)
				return err
			}
		}
	} else {
		err := os.MkdirAll(modelPath, os.ModePerm)
		if err != nil {
			// fmt.Println("Error:", err)
			return err
		}
		//ok we need to add some logic here now to analyze the model/dataset before we go into downloading

		//get root path files and folders
		err = processHFFolderTree(modelPath, IsDataset, SkipSHA, ModelDatasetName, ModelBranch, "") // passing empty as foldername, because its the first root folder
		if err != nil {
			// fmt.Println("Error:", err)
			return err
		}
	}

	return nil
}
func processHFFolderTree(ModelPath string, IsDataset bool, SkipSHA bool, ModelDatasetName string, Branch string, fodlerName string) error {
	JsonTreeVaraible := JsonModelsFileTreeURL //we assume its Model first
	RawFileURL := RawModelFileURL
	LfsResolverURL := LfsModelResolverURL
	AgreementURL := fmt.Sprintf(AgreementModelURL, ModelDatasetName)
	HasFilter := false
	var FilterBinFileString []string
	if strings.Contains(ModelDatasetName, ":") && !IsDataset {
		HasFilter = true
		//remove the filterd content from Model Name
		f := strings.Split(ModelDatasetName, ":")
		ModelDatasetName = f[0]
		FilterBinFileString = strings.Split(strings.ToLower(f[1]), ",")
		fmt.Printf("\nFilter Has been applied, will include LFS Model Files that contains: %s", FilterBinFileString)
	}
	if IsDataset {
		JsonTreeVaraible = JsonDatasetFileTreeURL //set this to true if it its set to Dataset
		RawFileURL = RawDatasetFileURL
		LfsResolverURL = LfsDatasetResolverURL
		AgreementURL = fmt.Sprintf(AgreementDatasetURL, ModelDatasetName)
	}

	tempFolder := path.Join(ModelPath, fodlerName, "tmp")
	// updated ver: 1.2.5; I cannot clear it if I'm trying to implement resume broken downloads based on a single file
	// if _, err := os.Stat(tempFolder); err == nil { //clear it if it exists before for any reason
	// 	err = os.RemoveAll(tempFolder)
	// 	if err != nil {
	// 		return err
	// 	}
	// }
	err := os.MkdirAll(tempFolder, os.ModePerm)
	if err != nil {
		// fmt.Println("Error:", err)
		return err
	}
	// updated ver: 1.2.5; I cannot clear it if I'm trying to implement resume broken downloads based on a single file
	// defer os.RemoveAll(tempFolder) //delete tmp folder upon returning from this function
	branch := Branch
	JsonFileListURL := fmt.Sprintf(JsonTreeVaraible, ModelDatasetName, branch, fodlerName)
	fmt.Printf("\nGetting File Download Files List Tree from: %s", JsonFileListURL)

	client := &http.Client{}
	req, err := http.NewRequest("GET", JsonFileListURL, nil)
	if err != nil {
		return err
	}
	if RequiresAuth {
		// Set the authorization header with the Bearer token
		bearerToken := AuthToken
		req.Header.Add("Authorization", "Bearer "+bearerToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		// fmt.Println("Error:", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 && RequiresAuth == false {
		return fmt.Errorf("\nThis Repo requires access token, generate an access token form huggingface, and pass it using flag: -t TOKEN")
	}
	if resp.StatusCode == 403 {
		return fmt.Errorf("\nYou need to manually Accept the agreement for this model/dataset: %s on HuggingFace site, No bypass will be implemeted", AgreementURL)
	}
	// Read the response body into a byte slice
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		// fmt.Println("Error:", err)
		return err

	}

	jsonFilesList := []hfmodel{}
	err = json.Unmarshal(content, &jsonFilesList)
	if err != nil {
		return err
	}
	for i := range jsonFilesList {
		jsonFilesList[i].AppendedPath = path.Join(ModelPath, jsonFilesList[i].Path)
		if jsonFilesList[i].Type == "directory" {
			jsonFilesList[i].IsDirectory = true
			err := os.MkdirAll(path.Join(ModelPath, jsonFilesList[i].Path), os.ModePerm)
			if err != nil {
				return err
			}
			jsonFilesList[i].SkipDownloading = true
			//now if this a folder, this whole function will be called again recursivley
			err = processHFFolderTree(ModelPath, IsDataset, SkipSHA, ModelDatasetName, Branch, jsonFilesList[i].Path) //recursive call
			if err != nil {
				return err
			}
			continue
		}

		jsonFilesList[i].DownloadLink = fmt.Sprintf(RawFileURL, ModelDatasetName, branch, jsonFilesList[i].Path)
		if jsonFilesList[i].Lfs != nil {
			jsonFilesList[i].IsLFS = true
			resolverURL := fmt.Sprintf(LfsResolverURL, ModelDatasetName, branch, jsonFilesList[i].Path)
			getLink, err := getRedirectLink(resolverURL)
			if err != nil {
				return err
			}
			//Check for filter
			if HasFilter {
				filenameLowerCase := strings.ToLower(jsonFilesList[i].Path)
				if strings.HasSuffix(filenameLowerCase, ".act") || strings.HasSuffix(filenameLowerCase, ".bin") ||
					strings.Contains(filenameLowerCase, ".gguf") || // either *.gguf or *.gguf-split-{a, b, ...}
					strings.HasSuffix(filenameLowerCase, ".safetensors") || strings.HasSuffix(filenameLowerCase, ".pt") || strings.HasSuffix(filenameLowerCase, ".meta") ||
					strings.HasSuffix(filenameLowerCase, ".zip") || strings.HasSuffix(filenameLowerCase, ".z01") || strings.HasSuffix(filenameLowerCase, ".onnx") || strings.HasSuffix(filenameLowerCase, ".data") ||
					strings.HasSuffix(filenameLowerCase, ".onnx_data") {
					jsonFilesList[i].FilterSkip = true //we assume its skipped, unless below condition range match
					for _, ff := range FilterBinFileString {
						if strings.Contains(filenameLowerCase, ff) {
							jsonFilesList[i].FilterSkip = false
						}
					}

				}
			}
			jsonFilesList[i].DownloadLink = getLink
		}
	}
	// UNCOMMENT BELOW TWO LINES TO DEBUG THIS FOLDER JSON STRUCTURE
	// s, _ := json.MarshalIndent(jsonFilesList, "", "  ")
	// fmt.Println(string(s))
	//2nd loop through the files, checking exists/non-exists
	for i := range jsonFilesList {
		//check if the file exists before
		// Check if the file exists
		if jsonFilesList[i].IsDirectory {
			continue
		}
		if jsonFilesList[i].FilterSkip {
			continue
		}
		filename := jsonFilesList[i].AppendedPath
		if _, err := os.Stat(filename); err == nil {
			// File exists, get its size
			fileInfo, _ := os.Stat(filename)
			size := fileInfo.Size()
			fmt.Printf("\nChecking Existsing file: %s", jsonFilesList[i].AppendedPath)
			//  for non-lfs files, I can only compare size, I don't there is a sha256 hash for them
			if size == int64(jsonFilesList[i].Size) {
				jsonFilesList[i].SkipDownloading = true
				if jsonFilesList[i].IsLFS {
					if !SkipSHA {
						err := verifyChecksum(jsonFilesList[i].AppendedPath, jsonFilesList[i].Lfs.Oid_SHA265)
						if err != nil {
							err := os.Remove(jsonFilesList[i].AppendedPath)
							if err != nil {
								return err
							}
							jsonFilesList[i].SkipDownloading = false
							fmt.Printf("\nHash failed for LFS file: %s, will redownload/resume", jsonFilesList[i].AppendedPath)
							return err
						}
						fmt.Printf("\nHash Matched for LFS file: %s", jsonFilesList[i].AppendedPath)
					} else {
						fmt.Printf("\nHash Matching SKIPPED for LFS file: %s", jsonFilesList[i].AppendedPath)
					}

				} else {
					fmt.Printf("\nfile size matched for non LFS file: %s", jsonFilesList[i].AppendedPath)
				}
			}

		}

	}
	//3ed loop through the files, downloading missing/failed files
	for i := range jsonFilesList {
		if jsonFilesList[i].IsDirectory {
			continue
		}
		if jsonFilesList[i].SkipDownloading {
			fmt.Printf("\nSkipping: %s", jsonFilesList[i].AppendedPath)
			continue
		}
		if jsonFilesList[i].FilterSkip {
			fmt.Printf("\nFilter Skipping: %s", jsonFilesList[i].AppendedPath)
			continue
		}
		// fmt.Printf("Downloading: %s\n", jsonFilesList[i].Path)
		if jsonFilesList[i].IsLFS {
			err := downloadFileMultiThread(tempFolder, jsonFilesList[i].DownloadLink, jsonFilesList[i].AppendedPath)
			if err != nil {
				return err
			}
			//lfs file, verify by checksum
			fmt.Printf("\nChecking SHA256 Hash for LFS file: %s", jsonFilesList[i].AppendedPath)
			if !SkipSHA {
				err = verifyChecksum(jsonFilesList[i].AppendedPath, jsonFilesList[i].Lfs.Oid_SHA265)
				if err != nil {
					err := os.Remove(jsonFilesList[i].AppendedPath)
					if err != nil {
						return err
					}
					//jsonFilesList[i].SkipDownloading = false
					fmt.Printf("\nHash failed for LFS file: %s", jsonFilesList[i].AppendedPath)
					return err
				}
				fmt.Printf("\nHash Matched for LFS file: %s", jsonFilesList[i].AppendedPath)

			} else {
				fmt.Printf("\nHash Matching SKIPPED for LFS file: %s", jsonFilesList[i].AppendedPath)
			}

		} else {
			// err := downloadFileMultiThread(tempFolder, jsonFilesList[i].DownloadLink, jsonFilesList[i].AppendedPath) //maybe later I'll enable multithreading for all files, even non-lfs
			err = downloadSingleThreaded(jsonFilesList[i].DownloadLink, jsonFilesList[i].AppendedPath) //no checksum available for small non-lfs files
			if err != nil {
				return err
			}
			//non-lfs file, verify by size matching
			fmt.Printf("\nChecking file size matching: %s", jsonFilesList[i].AppendedPath)
			if _, err := os.Stat(jsonFilesList[i].AppendedPath); err == nil {
				fileInfo, _ := os.Stat(jsonFilesList[i].AppendedPath)
				size := fileInfo.Size()
				if size != int64(jsonFilesList[i].Size) {
					return fmt.Errorf("\nFile size mismatch: %s, filesize: %d, Needed Size: %d", jsonFilesList[i].AppendedPath, size, jsonFilesList[i].Size)
				}
			} else {
				return fmt.Errorf("\nFile does not exist: %s", jsonFilesList[i].AppendedPath)
			}
		}
	}
	os.RemoveAll(tempFolder) //by here its safe to delete the temp folder
	return nil
}

// ***********************************************   All the functions below generated by ChatGPT 3.5, and ChatGPT 4 , with some modifications ***********************************************
func IsValidModelName(modelName string) bool {
	pattern := `^[A-Za-z0-9_\-]+/[A-Za-z0-9\._\-]+$`
	match, _ := regexp.MatchString(pattern, modelName)
	return match
}

func getRedirectLink(url string) (string, error) {

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if RequiresAuth {
				bearerToken := AuthToken
				req.Header.Add("Authorization", "Bearer "+bearerToken)
			}
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	if RequiresAuth {
		// Set the authorization header with the Bearer token
		bearerToken := AuthToken
		req.Header.Add("Authorization", "Bearer "+bearerToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 && RequiresAuth == false {
		return "", fmt.Errorf("This Repo requires access token, generate an access token form huggingface, and pass it using flag: -t TOKEN")
	}
	if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
		redirectURL := resp.Header.Get("Location")
		return redirectURL, nil
	}

	return "", fmt.Errorf("No redirect found")
}

func verifyChecksum(fileName string, expectedChecksum string) error {
	file, err := os.Open(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}

	sum := hasher.Sum(nil)
	if hex.EncodeToString(sum) != expectedChecksum {
		return fmt.Errorf("checksums do not match")
	}

	return nil
}

func downloadChunk(tempFolder string, outputFileName string, idx int, url string, start, end int64, wg *sync.WaitGroup, progress chan<- int64) error {
	defer wg.Done()

	tmpFileName := path.Join(tempFolder, fmt.Sprintf("%s_%d.tmp", outputFileName, idx))
	var compensationBytes int64 = 12

	// Checking file if exists
	if fi, err := os.Stat(tmpFileName); err == nil { // file exists
		// If file is already completely downloaded
		if fi.Size() == (end - start) {
			// Reflect progress and return
			progress <- fi.Size()
			return nil
		}

		// Fetching size to adjust start byte and compensate for potential corruption
		start = int64(math.Max(float64(start+fi.Size()-compensationBytes), 0.0))

		// Reflecting skipped part in progress, minus compensationBytes so we download them again. Making sure it does not go negative
		progress <- int64(math.Max(float64(fi.Size()-compensationBytes), 0.0))
	}

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	if RequiresAuth {
		// Set the authorization header with the Bearer token
		bearerToken := AuthToken
		req.Header.Add("Authorization", "Bearer "+bearerToken)
	}

	// Updating the Range header
	rangeHeader := fmt.Sprintf("bytes=%d-%d", start, end-1)
	req.Header.Add("Range", rangeHeader)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 && RequiresAuth == false {
		return fmt.Errorf("This Repo requires access token, generate an access token form huggingface, and pass it using flag: -t TOKEN")
	}

	// Open the file to append/add the new content
	tempFile, err := os.OpenFile(tmpFileName, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer tempFile.Close()

	// Seek to the beginning of the compensation part
	_, err = tempFile.Seek(-compensationBytes, 2) // SEEK_END is 2
	// If seek fails, it probably means the file size is less than compensationBytes
	if err != nil {
		_, err = tempFile.Seek(0, 0) // Seek to start of the file
		if err != nil {
			return err
		}
	}

	buffer := make([]byte, 1024)
	for {
		bytesRead, err := resp.Body.Read(buffer)
		if err != nil && err != io.EOF {
			return err
		}

		if bytesRead == 0 {
			break
		}

		_, err = tempFile.Write(buffer[:bytesRead])
		if err != nil {
			return err
		}

		progress <- int64(bytesRead)
	}

	return nil
}

func mergeFiles(tempFodler, outputFileName string, numChunks int) error {
	outputFile, err := os.Create(outputFileName)
	if err != nil {
		return err
	}
	defer outputFile.Close()

	for i := 0; i < numChunks; i++ {
		tmpFileName := fmt.Sprintf("%s_%d.tmp", path.Base(outputFileName), i)
		tempFileName := path.Join(tempFodler, tmpFileName)
		tempFiles, err := ioutil.ReadDir(tempFodler)
		if err != nil {
			return err
		}
		for _, file := range tempFiles {

			if matched, _ := filepath.Match(tempFileName, path.Join(tempFodler, file.Name())); matched {
				tempFile, err := os.Open(path.Join(tempFodler, file.Name()))
				if err != nil {
					return err
				}
				_, err = io.Copy(outputFile, tempFile)
				if err != nil {
					return err
				}
				err = tempFile.Close()
				if err != nil {
					return err
				}
				err = os.Remove(path.Join(tempFodler, file.Name()))
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func downloadFileMultiThread(tempFolder, url, outputFileName string) error {
	client := &http.Client{}
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return err
	}
	if RequiresAuth {
		// Set the authorization header with the Bearer token
		bearerToken := AuthToken
		req.Header.Add("Authorization", "Bearer "+bearerToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode == 401 && RequiresAuth == false {
		return fmt.Errorf("This Repo requires access token, generate an access token form huggingface, and pass it using flag: -t TOKEN")

	}
	contentLength, err := strconv.Atoi(resp.Header.Get("Content-Length"))
	if err != nil {
		return err
	}

	chunkSize := int64(contentLength / NumConnections)

	progress := make(chan int64, NumConnections)

	//update 1.2.5; we need to check now, if the tmp folder does exists, if the number of files exists before, matched the number of connection, we can proceed with the logic of resuming
	// Calculate the temp file name pattern.
	baseFileName := path.Base(outputFileName)
	tmpFileNamePattern := filepath.Join(tempFolder, fmt.Sprintf("%s_*.tmp", baseFileName))

	// Use Glob to find all files that match this pattern.
	matches, err := filepath.Glob(tmpFileNamePattern)
	if err != nil {
		fmt.Println(err)
		return err
	}

	// Print the number of matched files.
	// count := len(matches)
	if len(matches) > 0 {
		fmt.Printf("\nFound existing incomplete download for the file: %s\nForcing Number of connections to: %d\n\n", baseFileName, len(matches))
		NumConnections = len(matches)
	}
	wg := &sync.WaitGroup{}

	errChan := make(chan error)

	for i := 0; i < NumConnections; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize

		if i == NumConnections-1 {
			end = int64(contentLength)
		}
		wg.Add(1)
		go func(i int, start, end int64) {
			err := downloadChunk(tempFolder, path.Base(outputFileName), i, url, start, end, wg, progress)
			if err != nil {
				errChan <- fmt.Errorf("Error downloading chunk %d: %w", i, err)
			}
		}(i, start, end)
	}
	// Mark the start time of the download
	startTime := time.Now()
	go func() {
		var totalDownloaded int64

		// Calculate speed in megabytes per second
		fmt.Printf("\n\n")
		for chunkSize := range progress {
			totalDownloaded += chunkSize
			elapsed := time.Since(startTime).Seconds()
			speed := float64(totalDownloaded) / 1024 / 1024 / elapsed
			fmt.Printf("\rDownloading %s Speed: %.2f MB/sec, %.2f%% ", outputFileName, speed, float64(totalDownloaded*100)/float64(contentLength))
		}
	}()

	go func() {
		wg.Wait() // Wait for all downloadChunk to finish
		close(errChan)
	}()

	// Check if there was an error in any of the running routines
	for err := range errChan {
		if err != nil {
			fmt.Println(err) // Or however you want to handle the error
			// Here you can choose to return, exit, or however you want to stop going forward
			return err
		}
	}

	// fmt.Print("\nDownload completed")
	fmt.Printf("\nMerging %s Chunks", outputFileName)
	err = mergeFiles(tempFolder, outputFileName, NumConnections)
	if err != nil {
		return err
	}

	return nil
}
func downloadSingleThreaded(url, outputFileName string) error {
	outputFile, err := os.Create(outputFileName)

	if err != nil {
		return err
	}
	defer outputFile.Close()

	// Set the authorization header with the Bearer token

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Fatal(err)
	}
	if RequiresAuth {
		// Set the authorization header with the Bearer token
		bearerToken := AuthToken
		req.Header.Add("Authorization", "Bearer "+bearerToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode == 401 && RequiresAuth == false {
		return fmt.Errorf("This Repo requires access token, generate an access token form huggingface, and pass it using flag: -t TOKEN")

	}
	_, err = io.Copy(outputFile, resp.Body)
	if err != nil {
		return err
	}

	// fmt.Println("\nDownload completed")
	return nil
}
