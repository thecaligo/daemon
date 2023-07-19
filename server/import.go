package server

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/pterodactyl/wings/environment"
	"github.com/secsy/goftp"
	"golang.org/x/crypto/ssh"
)

type Runes []rune

// Import the serveur
func (s *Server) Import(sync bool, user string, password string, hote string, port int, srclocation string, dstlocation string, Type string) error {
	if sync {
		s.Log().Info("syncing server state with remote source before executing import process")
		if err := s.Sync(); err != nil {
			return err
		}
	}

	var err error
	s.Events().Publish(ImportStartedEvent, "")
	if Type == "ftp" || port == 21 {
		err = s.internalImportFtp(user, password, hote, port, srclocation, dstlocation)

	} else {
		err = s.internalImport(user, password, hote, port, srclocation, dstlocation)

	}

	s.Log().WithField("was_successful", err == nil).Debug("notifying panel of server import state")
	if serr := s.SyncImportState(err == nil); serr != nil {
		l := s.Log().WithField("was_successful", err == nil)

		// If the request was successful but there was an error with this request, attach the
		// error to this log entry. Otherwise ignore it in this log since whatever is calling
		// this function should handle the error and will end up logging the same one.
		if err == nil {
			l.WithField("error", serr)
		}

		l.Warn("failed to notify panel of server import state")
	}

	// Ensure that the server is marked as offline at this point, otherwise you end up
	// with a blank value which is a bit confusing.
	s.Environment.SetState(environment.ProcessOfflineState)

	// Push an event to the websocket so we can auto-refresh the information in the panel once
	// the install is completed.
	s.Events().Publish(ImportCompletedEvent, "")
	s.SyncImportState(err != nil)

	return err
}

// Import server
func (s *Server) ImportNew(user string, password string, hote string, port int, srclocation string, dstlocation string, Type string, Wipe bool) error {
	if s.Environment.State() != environment.ProcessOfflineState {
		s.Log().Debug("waiting for server instance to enter a stopped state")
		if err := s.Environment.WaitForStop(s.Context(), time.Second*10, true); err != nil {
			return err
		}
	}
	if Wipe {
		cleaned, err := s.fs.SafePath(".")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Can t remove server file: %v\n", err)
		}
		os.RemoveAll(cleaned)
		os.MkdirAll(cleaned, 0777)
	}
	if !strings.HasSuffix(dstlocation, "/") {
		dstlocation = dstlocation + "/"
	}

	if Type == "sftp" {
		if !strings.HasPrefix(srclocation, "/") {
			srclocation = "/" + srclocation
		}
		if !strings.HasSuffix(srclocation, "/") {
			srclocation = srclocation + "/"
		}
	} else {

		if !strings.HasPrefix(srclocation, "/") {

			srclocation = "/" + srclocation
		}
	}

	return s.Import(true, user, password, hote, port, srclocation, dstlocation, Type)
}

/*
*
*
*	ONLY FOR SFTP
*
*
 */
// Internal import function used to simplify reporting back to the Panel.
func (s *Server) internalImport(user string, password string, hote string, port int, srclocation string, dstlocation string) error {

	s.Log().Info("beginning import process for server")
	if err := s.ServerImporter(user, password, hote, port, srclocation, dstlocation); err != nil {
		return err
	}
	s.Log().Info("completed import process for server")
	return nil
}

func (s *Server) ServerImporter(user string, password string, hote string, port int, srclocation string, dstlocation string) error {
	config := ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},

		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	cleaned, err := s.fs.SafePath(dstlocation)
	os.MkdirAll(cleaned, 0777)

	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", hote, port)
	// Connect to server
	conn, err := ssh.Dial("tcp", addr, &config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connecto to [%s]: %v\n", addr, err)
		return err
	}
	sc, err := sftp.NewClient(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to start SFTP subsystem: %v\n", err)
		return err
	}
	files, err := sc.ReadDir(srclocation)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to list remote dir: %v\n", err)
		return err
	}

	for _, f := range files {
		var name string

		name = f.Name()

		if f.IsDir() {
			strRune := Runes(name)
			reversed := strRune.ReverseString()
			slashnumber := strings.Index(string(name), "/")
			if string(reversed[slashnumber+1]) != "" {

				os.MkdirAll(cleaned+"/"+srclocation+name, 0777)
			}
			if err := isdir("."+srclocation+name+"/", sc, cleaned, srclocation, dstlocation); err != nil {
				return err
			}

		}
		if !f.IsDir() {

			if err := downloadfilesfromsftpserver(name, sc, cleaned, srclocation); err != nil {
				return err
			}
		}
	}
	return nil

}

func isdir(dir string, sc *sftp.Client, cleaned string, srclocation string, dstlocation string) error {
	files, err := sc.ReadDir(dir)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to list remote dir: %v\n", err)
		return err
	}
	for _, f := range files {
		var name string

		name = f.Name()

		if f.IsDir() {
			strRune := Runes(name)
			reversed := strRune.ReverseString()
			slashnumber := strings.Index(string(name), "/")
			if string(reversed[slashnumber+1]) != "" {
				os.MkdirAll(cleaned+dir+name, 0777)

			}
			isdir(dir+name+"/", sc, cleaned, srclocation, dstlocation)
		}
		if !f.IsDir() {
			afterlastslash := strings.Split(name, "/")
			test := strings.Join(afterlastslash[len(afterlastslash)-1:], "")
			slashnumber := strings.ReplaceAll(dir+name, test, "")
			os.MkdirAll(cleaned+"/"+slashnumber, 0777)
			// Output each file name and size in bytes
			// Note: SFTP To Go doesn't support O_RDWR mode

			if err := downloadfilesfromsftpserver(name, sc, cleaned, dir); err != nil {
				return err
			}
		}

	}
	return nil
}
func (str Runes) ReverseString() (revStr Runes) {
	l := len(str)
	revStr = make(Runes, l)
	for i := 0; i <= l/2; i++ {
		revStr[i], revStr[l-1-i] = str[l-1-i], str[i]
	}
	return revStr
}
func downloadfilesfromsftpserver(name string, sc *sftp.Client, folder string, srcfolder string) error {
	// Note: SFTP To Go doesn't support O_RDWR mode
	srcFile, err := sc.OpenFile(srcfolder+name, (os.O_RDONLY))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to open remote file: %v\n", err)
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(strings.ReplaceAll(strings.ReplaceAll(folder+"/"+srcfolder+name, "//", "/"), "./", ""))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to open local file: %v\n", err)
		return err
	}
	defer dstFile.Close()
	bytes, err := io.Copy(dstFile, srcFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to download remote file: %v\n and %v", err, bytes)
		return err
	}
	return nil
}
func (s *Server) SyncImportState(successful bool) error {
	return s.client.SetImportStatus(s.Context(), s.ID(), successful)
}

/*
*
*
*
*
*
*	ONLY FOR FTP
*
*
*
*
*
*
*

 */
// Internal import function used to simplify reporting back to the Panel.
func (s *Server) internalImportFtp(user string, password string, hote string, port int, srclocation string, dstlocation string) error {

	s.Log().Info("beginning import process for server")
	if err := s.ServerImporterFtp(user, password, hote, port, srclocation, dstlocation); err != nil {
		return err
	}
	s.Log().Info("completed import process for server")
	return nil
}

func (s *Server) ServerImporterFtp(user string, password string, hote string, port int, srclocation string, dstlocation string) error {
	config := goftp.Config{
		User:               user,
		Password:           password,
		ConnectionsPerHost: 10,
		Timeout:            10 * time.Second,
	}

	cleaned, err := s.fs.SafePath(dstlocation)
	os.MkdirAll(cleaned, 0777)

	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", hote, port)
	// Connect to server
	sc, err := goftp.DialConfig(config, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connecto to [%s]: %v\n", addr, err)
		return err
	}
	files, err := sc.ReadDir("." + srclocation)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to list remote ftp dir: %v\n", err)
		return err
	}

	for _, f := range files {

		var name string

		name = f.Name()
		if f.IsDir() {
			strRune := Runes(name)
			reversed := strRune.ReverseString()
			slashnumber := strings.Index(string(name), "/")
			if string(reversed[slashnumber+1]) != "" {
				os.MkdirAll(cleaned+"/"+name, 0777)
			}
			if err := isdirFtp(name+"/", sc, cleaned, srclocation, dstlocation); err != nil {
				return err
			}

		}
		if !f.IsDir() {

			if err := downloadfilesfromftpserver(name, sc, cleaned, "", srclocation, dstlocation); err != nil {
				return err
			}
		}
	}
	return nil

}

func isdirFtp(dir string, sc *goftp.Client, cleaned string, srclocation string, dstlocation string) error {
	files, err := sc.ReadDir("./" + srclocation + "/" + dir)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to list remote ftp isdir: %v\n", err)
		return err
	}
	for _, f := range files {
		var name string

		name = f.Name()

		if f.IsDir() {
			strRune := Runes(name)
			reversed := strRune.ReverseString()
			slashnumber := strings.Index(string(name), "/")
			if string(reversed[slashnumber+1]) != "" {

				os.MkdirAll(cleaned+"/"+dir+name, 0777)
			}
			isdirFtp(dir+name+"/", sc, cleaned, srclocation, dstlocation)
		}
		if !f.IsDir() {
			afterlastslash := strings.Split(name, "/")
			test := strings.Join(afterlastslash[len(afterlastslash)-1:], "")
			slashnumber := strings.ReplaceAll(dir+"/"+name, test, "")

			os.MkdirAll(cleaned+"/"+slashnumber, 0777)
			// Output each file name and size in bytes
			// Note: SFTP To Go doesn't support O_RDWR mode

			if err := downloadfilesfromftpserver(name, sc, cleaned, dir, srclocation, dstlocation); err != nil {
				return err
			}
		}

	}
	return nil
}
func downloadfilesfromftpserver(name string, sc *goftp.Client, folder string, srcfolder string, srclocation string, dstlocation string) error {

	// Note: SFTP To Go doesn't support O_RDWR mode
	dstFile, err := os.Create(strings.Replace(folder+"/"+srcfolder+"/"+name, "//", "/", -1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to open local file: %v\n", err)
		return err
	}
	defer dstFile.Close()
	errr := sc.Retrieve("."+srclocation+"/"+srcfolder+name, dstFile)
	if errr != nil {
		fmt.Fprintf(os.Stderr, "Unable to download remote file: %v\n", errr)
		return errr
	}
	return nil
}
