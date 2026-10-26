package licensechecker

import (
	"EtherCAT/constants"
	"EtherCAT/helper"
	"EtherCAT/logger"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"
)

const encryptKey = constants.EncryptKey
const projectCode = constants.ProjectCode
const licenseServiceURL = constants.LicenseServiceURL
const licenseFilePath = "/license.lic"

//License store the license details
type License struct {
	DeviceID    string `json:"deviceId"`
	CreatedDate string `json:"createdDate"`
	CreatedBy   string `json:"createdBy"`
	ProjectCode string `json:"projectId"`
	LicenseKey  string `json:"licenseKey"`
	ExpiryDate  string `json:"expiryDate"`
	Action      string `json:"-"`
}

//CheckLicense check the license of the system
//   revalidateLicense: if true then a go routine spawned up to check whether the license still valid.
//   This is now enabled when starting of the application.
func CheckLicense(revalidateLicense bool) error {
	if !isLicenseFileExist(helper.AppendWDPath(licenseFilePath)) {
		return createLicenseFile()
	}
	return validateLicenseFile(revalidateLicense)
}

func isLicenseFileExist(licenseFilePath string) bool {
	if _, err := os.Stat(licenseFilePath); err == nil {
		return true
	} else if os.IsNotExist(err) {
		return false
	}
	return false
}

func createLicenseFile() error {
	logger.Info("fetching license details from server...")
	serial, _ := helper.ReadSerialNumber()

	licenseOut, err := fetchLicense(serial)
	if err != nil {
		return err
	}
	if licenseOut.DeviceID != serial {
		return errors.New("Unable to find any license for this device")
	}
	inBytes, err := json.Marshal(licenseOut)
	if err != nil {
		return err
	}
	logger.Info("creating license file")
	//encrypt license details using the key retrieved from license server
	encryptedLic := helper.Encrypt(inBytes, licenseOut.LicenseKey)
	writeToFile(encryptedLic, helper.AppendWDPath("/license.lic"))

	//encrypt the key returned from server with another key. This will reduce the time to get the key from server then
	//decrypt the license file
	licenseKey := helper.Encrypt([]byte(licenseOut.LicenseKey), encryptKey)
	writeToFile(licenseKey, helper.AppendWDPath("/license.key"))
	logger.Info("license file created successfully")
	return nil
}

func validateLicenseFile(revalidateLicense bool) error {
	encryptedLicenseKey, err := readFromFile(helper.AppendWDPath("/license.key"))
	if err != nil {
		return err
	}
	decryptedKey := helper.Decrypt(encryptedLicenseKey, encryptKey)

	encryptedLicense, err := readFromFile(helper.AppendWDPath("/license.lic"))
	if err != nil {
		return err
	}
	decryptedLicStr := helper.Decrypt(encryptedLicense, decryptedKey)
	var licenseOut License
	json.Unmarshal([]byte(decryptedLicStr), &licenseOut)
	serial, _ := helper.ReadSerialNumber()
	if licenseOut.DeviceID == serial {
		logger.Debug("license validated successfully")
		if revalidateLicense {
			go checkStillLicenseValid(serial)
		}
		return nil
	}
	return errors.New("License is invalid or expired")
}

func checkStillLicenseValid(serial string) {
	license, err := fetchLicense(serial)
	if err != nil {
		logger.Error(err)
		//in case of any error in fetching license then
		//return without any changes to current license
		return
	}
	str := license.ExpiryDate
	//play with the below commented code to find the expiry
	expiry, _ := time.Parse(time.RFC3339, str)
	logger.Trace("check license is still valid", license.DeviceID)

	hasExpired := false
	if expiry.Before(time.Now()) {
		//the license expiry date is less than todays date, so its expired
		hasExpired = true
	}
	if license.DeviceID != serial || hasExpired {
		os.Remove(helper.AppendWDPath("/license.lic"))
		os.Remove(helper.AppendWDPath("/license.key"))
		logger.Trace("license is not valid")
	}
	logger.Trace("license is valid")
}

/**
https://play.golang.org/
package main

import (
	"fmt"
	"time"
)

func inTimeSpan(start, end, check time.Time) bool {
	return check.After(start) && check.Before(end)
}

func main() {
	expiry, _ := time.Parse(time.RFC3339, "2022-01-20T10:09:44Z")
	if expiry.After(time.Now()) {
		fmt.Println("expired")
	}
}
**/

func fetchLicense(serial string) (License, error) {
	url := fmt.Sprintf("%s?deviceid=%s&projectid=%s", licenseServiceURL, serial, projectCode)
	resp, err := http.Get(url)
	if err != nil {
		logger.Error("unable to connect to ", licenseServiceURL)
		logger.Error(err)
		return License{}, errors.New("Unable to connect to licensing server")
	}
	defer resp.Body.Close()
	if err := HasError(resp); err != nil {
		return License{}, err
	}

	bodyBytes, _ := ioutil.ReadAll(resp.Body)
	var licenseOut License
	json.Unmarshal(bodyBytes, &licenseOut)
	return licenseOut, nil
}

func writeToFile(data, file string) {
	ioutil.WriteFile(file, []byte(data), 777)
}

func readFromFile(file string) ([]byte, error) {
	data, err := ioutil.ReadFile(file)
	return data, err
}
