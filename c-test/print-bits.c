#include <stdio.h>
#include <stdlib.h>
#include <malloc.h>
int *longToBinary(unsigned long int decimal)
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

int * decimalToBinary(int decimal)
{
  int binaryNumber[100],i=1,j,k=0;
  int quotient, len;
  int* b = malloc(sizeof(int) * 64);
  quotient = decimal;

  while(quotient!=0){
       binaryNumber[i++]= quotient % 2;
       quotient = quotient / 2;
  }
  len = i-1;
  for(j = i -1 ;j> 0;j--){
    b[k++] = binaryNumber[j];
  }
  for(j=i; j<64; j++){
    b[j] = 0;
  }
  return b;
}

main() {
  int chr = 0x0006;
  unsigned char a = chr;
  int i;

  int *lngToBin = decimalToBinary(a);
  printf("Value long to bin: %d\n", *lngToBin );


  printf("unsigned char: %c\n", a); 
  for (i = 0; i < 8; i++) {
      printf("%d", !!((a << i) & 0x80));
  }
  printf("\n");
  int directVal=64;
  for (i = 0; i < 8; i++) {
      printf("%d", !!((directVal << i) & 0x80));
  }
  printf("\n");

  return 0;
}



