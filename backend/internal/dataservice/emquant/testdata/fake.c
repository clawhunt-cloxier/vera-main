#include <string.h>
typedef struct { char *p; unsigned n; } eqchar;
typedef struct { eqchar *p; unsigned n; } eqchars;
typedef union { char c; _Bool b; short s; unsigned short us; int i; unsigned ui; long long ll; unsigned long long ull; float f; double d; } eqvalue;
typedef struct { int type; eqvalue value; eqchar text; } eqvariant;
typedef struct { eqvariant *p; unsigned n; } eqvariants;
typedef struct { eqchars codes, indicators, dates; eqvariants values; } eqdata;
typedef struct { char user[255], password[255]; } eqlogin;
typedef struct { int version, type, err, request, serial; eqdata *data; } eqmsg;
typedef int (*eq_log_callback)(const char *);
typedef int (*eq_data_callback)(const eqmsg *, void *);
static eq_data_callback main_callback;
void setserverlistdir(const char *s) { (void)s; }
int setcallback(eq_data_callback p) { main_callback = p; return p ? 0 : 1; }
int start(eqlogin *p, const char *s, eq_log_callback cb) {
 eqlogin *login = p;
 return strcmp(login->user, "user") || strcmp(login->password, "pass") || !s || !strstr(s,"LANGUAGEVERSION=503") || !cb ? 77 : 0;
}
int stop(void) { return 0; }
int releasedata(void *p) { return p ? 0 : 1; }
int csd(const char *codes, const char *indicators, const char *from, const char *to, const char *options, eqdata **out) {
 (void)indicators; (void)from; (void)to; (void)options;
 if (!strcmp(codes, "ERROR")) return 88;
 static eqchar code[] = {{"A",2},{"B",2}}, indicator[] = {{"X",2},{"Y",2}}, date[] = {{"D1",3},{"D2",3}};
 static eqvariant values[] = {{.type=6,.value.i=0},{.type=6,.value.i=1},{.type=6,.value.i=2},{.type=6,.value.i=3},{.type=6,.value.i=4},{.type=6,.value.i=5},{.type=6,.value.i=6},{.type=0}};
 static eqdata data;
 data = (eqdata){{code,2},{indicator,2},{date,2},{values,8}};
 if (!strcmp(codes, "TRUNCATED")) data.values.n = 7;
 if (!strcmp(codes, "BADUTF")) { static eqchar bad[] = {{"\xff",2}}; data.codes = (eqchars){bad,1}; }
 if (!strcmp(codes, "BADLENGTH")) { static eqchar bad[] = {{"AB",2}}; data.codes = (eqchars){bad,1}; }
 if (!strcmp(codes, "DISCONNECT")) { eqmsg event = {.type=0, .err=10002002}; main_callback(&event, 0); }
 *out = &data;
 return 0;
}
int css(const char *codes, const char *indicators, const char *options, eqdata **out) {
 return csd(codes, indicators, "", "", options, out);
}
int csqsnapshot(const char *codes, const char *indicators, const char *options, eqdata **out) {
 return csd(codes, indicators, "", "", options, out);
}
int cfn(const char *codes, const char *content, int mode, const char *options, eqdata **out) {
 if (mode != 1 && mode != 2) return 99;
 return csd(codes, content, "", "", options, out);
}
int tradedates(const char *from, const char *to, const char *options, eqdata **out) {
 (void)to; (void)options;
 if (!strcmp(from, "ERROR")) return 88;
 static eqchar code[] = {{"",1}}, indicator[] = {{"TRADEDATE",10}}, date[] = {{"2026/11/9",10},{"2026/11/10",11}};
 static eqvariant values[] = {{.type=13,.text={"2026/11/9",10}},{.type=13,.text={"2026/11/10",11}}};
 static eqdata data;
 data = (eqdata){{code,1},{indicator,1},{date,2},{values,2}};
 *out = &data;
 return 0;
}
