#include <ecrt.h>
#include <stdio.h>
#include <pthread.h>
// #include "globals.h"
// #include "master.h"
// #include "device.h"
///how to compile this program
//gcc -pthread -o ectest -Wall test-run.c -I/opt/etherlab/include -I/home/pi/etherlabmaster/etherlabmaster-code -I/home/pi/etherlabmaster/etherlabmaster-code/master /opt/etherlab/lib/libethercat.a
ec_master_t *master0=NULL;
pthread_t tid[3];

int * longToBinary(unsigned long int decimal)
{
  int binaryNumber[100],i=1,j,k=0;
  int len;
  unsigned long int quotient;
  int* b = malloc(sizeof(int) * 64);
  int* c = malloc(sizeof(int) * 64);
  quotient = decimal;

  while(quotient!=0){
       binaryNumber[i++]= quotient % 2;
       quotient = quotient / 2;
  }
  len = i-1;
  for(j = i -1 ;j> 0;j--){
    //b[k++] = binaryNumber[j];
    k++;
    b[len-k] = binaryNumber[j];
  }
  for(j=i; j<64; j++){
    b[j] = 0;
  }
  return b;
}

int* readInputSignal(ec_master_t *master0){
  unsigned long int v = 0xFFFFFFFF;
  uint32_t abortCode = 0;
  int errorCode = 0;
  size_t resultSize = 0;
  int k;
  int *resp;
  char res[32];
  char out_stat[4];
    ////printf("Inside Read Input Signal\n");
  if(ecrt_master_sdo_upload(master0, 0, 0x60FD,0, (unsigned char *)&v, sizeof(v), &resultSize, &abortCode) <0 ){
    printf("\n\n\n-----ALERT:: FAILURE------\n\n\n");
  }
  resp = longToBinary(v);
  return resp;
}

// func currentPosition(pos int32) float64 {
// 	homingOffset := 0.196
// 	// gearRatio := 90.00
// 	factorBacklash := 0.00
// 	backLash := 0.00
// 	angleWithPitchError := 0.00
// 	driveXRatio := 20000

// 	driveOffset := homingOffset * float64(driveXRatio)
// 	drivePosition := float64(pos) - driveOffset
// 	drivePosition = drivePosition / 20000
// 	drivePosition = float64(int(drivePosition) % 360)
// 	if drivePosition < 0 {
// 		drivePosition = float64(drivePosition + 360)
// 	}
// 	drivePosition = drivePosition + (factorBacklash * backLash) - angleWithPitchError
// 	return drivePosition
// }

int currentPosition(int pos) {
  float homingOffset = 0.196;
  int driveXRatio = 20000;
  float driveOffset = homingOffset * driveXRatio;
  float drivePosition = pos - driveOffset;
  drivePosition = drivePosition/20000;
  drivePosition = ((int)drivePosition) % 360;
  if(drivePosition<0) {
    drivePosition = drivePosition + 360;
  }
  return drivePosition;

}

void pollStatus(){
    unsigned int stat = 0xFFFF;
    char str[50];
    size_t res_size = 0;
    uint32_t abt_code = 0;
    char snum[32];
    int err = 0;
    float pos;
    char buffer [50];
    char *st1, *str2;
    unsigned int old_pos_1=999;
    unsigned int old_pos_2=999;
    int char_count=0;
    int errorCode = 0;
    int fail_count = 0;

    //char str[50];
    while(1){
      // master0 = drive_master[0];
      char_count = 0;
      ////printf("Inside Poll Status\n");
      errorCode = ecrt_master_sdo_upload(master0, 0, 0x6064, 0, (unsigned char *)&stat, sizeof(stat), &res_size, &abt_code);
      if(errorCode < 0){
        fail_count++;
        if(fail_count > 10){
            printf("Unable to communicate with EtherCAT.. Shutting Down..");
            exit(-1);
        }
      }
      fail_count = 0;
      printf("Drive location %d\n", currentPosition(stat));
      usleep(1000);
    }
}

//https://github.com/liangyaozhan/ethercat/blob/master/examples/mini/mini.c
//for any clue to find whether master connected
int main() {
    printf("test \n");
    int errorCode = 0;
    unsigned long maxFlowingError;
    uint32_t abortCode = 0;
    // return 0;
    // master0=ecrt_open_master(0);
    // if(!master0 || master0==NULL) {
    //     printf("no master found \n");
    //     return 0;
    // }else {
    //     printf("master found \n");
    // }
    ec_master_t *master = ecrt_request_master(0);
    
    if(!master || master==NULL) {
        printf("no master found \n");
        return 0;
    }else {
        printf("master found \n");
    }
    int result=0;
    ec_master_info_t tmp;
    result = ecrt_master(master, &tmp);
    if(result<0) {
        printf("ecrt_master no master found \n");
        return 0;
    }

    ec_pdo_info_t master_info;
    result = ecrt_master_get_pdo(master, 0, 0, 0, &master_info);
    if(result<0) {
        printf("ecrt_master_get_pdo no master found \n");
        return 0;
    }
    // ecrt_master_state( master, &state );
    // if(!master->link_up) {
    //           printf("no master found \n");
    //     return 0;
    // }

    // if (!master->device) {
    //           printf("no master found \n");
    //     return 0;
    // }
    
    ec_domain_t *domain0 = ecrt_master_create_domain(master);
    if(!domain0) {
        printf("no master found \n");
        return 0;
    }
    ec_slave_config_t *slaveConfig = ecrt_master_slave_config(master, 0, 0, 0x0000066f, 0x535300a1);
    if(!slaveConfig) {
        printf("no master found \n");
        return 0;
    }

    uint16_t drive_mode = 2;
    errorCode = ecrt_master_sdo_download(master, 0, 0x60F2, 0, (unsigned char *)&drive_mode, sizeof(drive_mode), &abortCode);
    printf("Set Drive to Absolute MODE %d\n", errorCode);

    return 0;

    uint16_t value = 0x0006;
    
    errorCode = ecrt_master_sdo_download(master0, 0, 0x6040, 0, (unsigned char *)&value, sizeof(value), &abortCode);
    usleep(100000);
    printf("Drive init complete %d\n", errorCode);

    value = 0x0007;
    ecrt_master_sdo_download(master0, 0, 0x6040, 0, (unsigned char *)&value, sizeof(value), &abortCode);
    usleep(100000);
    printf("Drive magnetize complete %d\n", errorCode);

    value = 0x004f;
    ecrt_master_sdo_download(master0, 0, 0x6040, 0, (unsigned char *)&value, sizeof(value), &abortCode);
    usleep(100000);
    printf("Drive Power on complete %d\n", errorCode);

    value = 0x000F;
    errorCode = ecrt_master_sdo_download(master0, 0, 0x6040, 0, (unsigned char *)&value, sizeof(value), &abortCode);
    printf("Drive Power on complete-1 %d\n", errorCode);

    int counter=300;
    int *registry;
    // while(1){
    //     counter--;
    //     if(counter < 0){
    //       printf("De-Clamp error\n");
    //       return;
    //     }
    //     registry = readInputSignal(master0);
    //     printf("%d",registry[21]);
    //     if(registry[21] == 1){
    //       printf("Got De-Clamp Signal. Now next step\n");
    //       break;
    //     }
    //     usleep(1000);
    //   }

    //base on manual_jog function in drive_interface.c
    unsigned long rpm = 1200000;
    size_t data_size=sizeof(rpm);
    maxFlowingError = 100000000;

    data_size = sizeof(maxFlowingError);
    errorCode = ecrt_master_sdo_download(master0, 0, 0x6083,0, (unsigned char *)&maxFlowingError, data_size, &abortCode);
    printf("PROFILE_ACCELERATION %d\n", errorCode);

    maxFlowingError = 100000000;
    data_size = sizeof(maxFlowingError);
    ecrt_master_sdo_download(master0, 0, 0x6084,0, (unsigned char *)&maxFlowingError, data_size, &abortCode);
    printf("PROFILE_DECELERATION %d\n", errorCode);

    int err;
    // err = pthread_create(&(tid[0]), NULL, &pollStatus, NULL);

    data_size = sizeof(rpm);
    errorCode = ecrt_master_sdo_download(master0, 0, 0x60FF,0, (unsigned char *)&rpm, data_size, &abortCode);
    printf("JOG IN +ve %d\n", errorCode);

    uint8_t cw_value = 0x3;
    data_size = sizeof(cw_value);
    errorCode = ecrt_master_sdo_download(master0, 0, 0x6060,0, (unsigned char*)&cw_value, data_size, &abortCode);
    printf("OPERATION_MODE %d\n",errorCode);

    rpm=1200000;
    data_size = sizeof(rpm);
    errorCode = ecrt_master_sdo_download(master0, 0, 0x60FF,0, (unsigned char *)&rpm, data_size, &abortCode);
    printf("JOG IN +ve %d\n", errorCode);

    // pthread_join(tid[0], NULL);

    return 0;
}