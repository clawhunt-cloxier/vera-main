#define csd old_csd
#define css old_css
#include "fake.c"
#undef csd
#undef css
int css(const char *code, const char *fields, const char *options, eqdata **out) {
 (void)options;
 if(strcmp(fields,"NAME,LISTDATE,DELISTDATE"))return 99;
 static eqchar codes[1],ind[]={{"NAME",5},{"LISTDATE",9},{"DELISTDATE",11}},dates[]={{"2026-01-01",11}};
 static eqvariant values[]={{.type=13,.text={"Synthetic Brief",16}},{.type=13,.text={"2000-01-01",11}},{.type=0}};
 static eqdata data;
 codes[0]=(eqchar){(char*)code,strlen(code)+1};
 data=(eqdata){{codes,1},{ind,3},{dates,1},{values,3}};*out=&data;return 0;
}
int csd(const char *code,const char *fields,const char *from,const char *to,const char *options,eqdata **out){
 (void)from;(void)options;
 if(strcmp(fields,"VOLUME,AMOUNT,TURN"))return 99;
 static eqchar codes[1],ind[]={{"VOLUME",7},{"AMOUNT",7},{"TURN",5}},dates[1];
 static eqvariant values[]={{.type=8,.value.ll=120000},{.type=11,.value.d=1800000.25},{.type=11,.value.d=1.25}};
 static eqdata data;
 codes[0]=(eqchar){(char*)code,strlen(code)+1};dates[0]=(eqchar){(char*)to,strlen(to)+1};
 data=(eqdata){{codes,1},{ind,3},{dates,1},{values,3}};*out=&data;return 0;
}
