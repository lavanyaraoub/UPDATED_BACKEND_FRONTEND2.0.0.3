#include<stdio.h> 
#include <string.h>
#include <stdlib.h>
#include <unistd.h> /* for fork */
#include <sys/types.h> /* for pid_t */
#include <sys/wait.h> /* for wait */

//gcc -o clxcall multiparam.c
//./multi >f1>f2 writes the output of system() to the file f2
int main (int argc, char *argv[])
{
    char syscall[255] = "hello world with a lengthy one";
    sprintf (syscall, "%s %d %s ", "./clx", 11, "'hello$rtt'");
    system(syscall);
    // printf("argv 1: %s",argv[1]);
    // printf("argv 2: %s",argv[2]);
    // /*Spawn a child to run the program.*/
    // pid_t pid=fork();
    // if (pid==0) { /* child process */
    //     static char *argv[]={"echo","Foo is my name.",NULL};
    //     static char argv2[200]="echo Foo is my name. sony";
    //     execv("/bin/echo",argv2);
    //     exit(127); /* only if execv fails */
    // }
    // else { /* pid!=0; parent process */
    //     waitpid(pid,0,0); /* wait for child to exit */
    // }
}