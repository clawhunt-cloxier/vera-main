#include <string.h>
typedef struct { char *p; unsigned n; } eqchar;
typedef struct { eqchar *p; unsigned n; } eqchars;
typedef union { char c; _Bool b; short s; unsigned short us; int i; unsigned ui; long long ll; unsigned long long ull; float f; double d; } eqvalue;
typedef struct { int type; eqvalue value; eqchar text; } eqvariant;
typedef struct { eqvariant *p; unsigned n; } eqvariants;
typedef struct { eqchars codes, indicators, dates; eqvariants values; } eqdata;
typedef struct { char user[255], password[255]; } eqlogin;
int setserverlistdir(const char *s) { return s != 0; }
int setcallback(void *p) { return p != 0; }
int start(void *p, const char *s, void *cb) {
 eqlogin *login = p;
 return strcmp(login->user, "user") || strcmp(login->password, "pass") || !s || !cb ? 77 : 0;
}
int stop(void) { return 0; }
int releasedata(void *p) { return p ? 0 : 1; }
int csd(const char *codes, const char *indicators, const char *from, const char *to, const char *options, void *out) {
 (void)indicators; (void)from; (void)to; (void)options;
 if (!strcmp(codes, "ERROR")) return 88;
 static eqchar code[] = {{"A",1},{"B",1}}, indicator[] = {{"X",1},{"Y",1}}, date[] = {{"D1",2},{"D2",2}};
 static eqvariant values[] = {{.type=6,.value.i=0},{.type=6,.value.i=1},{.type=6,.value.i=2},{.type=6,.value.i=3},{.type=6,.value.i=4},{.type=6,.value.i=5},{.type=6,.value.i=6},{.type=0}};
 static eqdata data;
 data = (eqdata){{code,2},{indicator,2},{date,2},{values,8}};
 if (!strcmp(codes, "TRUNCATED")) data.values.n = 7;
 *(eqdata**)out = &data;
 return 0;
}
