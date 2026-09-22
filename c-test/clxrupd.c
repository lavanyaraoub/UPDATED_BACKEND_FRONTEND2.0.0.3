#include<stdio.h> 
#include <string.h>
#include <stdlib.h>
#include <unistd.h> /* for fork */
#include <sys/types.h> /* for pid_t */
#include <sys/wait.h> /* for wait */
int main (int argc, char *argv[])
{
    printf("argv 1: %s \n",argv[1]);
    printf("argv 2: %s \n",argv[2]);
    printf("argv 3: %s \n",argv[3]);
}