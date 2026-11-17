/* Synthetic ABI fixture only. These states establish no vendor semantics. */
#define csqsnapshot old_csqsnapshot
#include "fake.c"
#undef csqsnapshot
#include <time.h>
#include <stdio.h>

int csqsnapshot(const char *codes, const char *fields, const char *options, eqdata **out) {
 (void)options;
 if (strcmp(fields, "TIME,NOW,TESTSTATUS")) return 99;
 time_t now = time(NULL) + 8 * 3600;
 if (!strcmp(codes,"600002.SH")) now -= 120;
 struct tm local;
 gmtime_r(&now, &local);
 static char date[11];
 snprintf(date, sizeof(date), "%04d-%02d-%02d",local.tm_year+1900,local.tm_mon+1,local.tm_mday);
 static eqchar code[1], dates[1], indicators[]={{"TIME",5},{"NOW",4},{"TESTSTATUS",11}};
 static eqvariant values[3];
 static eqdata data;
 code[0]=(eqchar){(char*)codes,strlen(codes)+1}; dates[0]=(eqchar){date,11};
 values[0]=(eqvariant){.type=6,.value.i=local.tm_hour*10000+local.tm_min*100+local.tm_sec};
 values[1]=(eqvariant){.type=11,.value.d=10.125};
 values[2]=(eqvariant){.type=13,.text={"OPEN",5}};
 if (!strcmp(codes,"600001.SH")) values[2]=(eqvariant){.type=13,.text={"CLOSED",7}};
 data=(eqdata){{code,1},{indicators,3},{dates,1},{values,3}}; *out=&data;
 return 0;
}
