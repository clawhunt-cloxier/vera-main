package emquant

import "testing"

func TestQueryOptionsRejectSyntheticMissingValuesAndSecrets(t *testing.T) {
	for _, input := range []string{"SHOWBLANK=0", " showblank = -1 ", "FillData=1", "filldata=true", "PASSWORD=secret", "Api_Key=secret", "Period=1,period=2", "Period", "Period=", "Bad name=1", "Period=1;FillData=1", "Period=1\nFillData=1"} {
		if err := validateOptions(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for _, input := range []string{"", "Period=1,AdjustFlag=1,CurType=2", "Market=CNSESH,Period=1,Order=1", "filldata=0"} {
		if err := validateOptions(input); err != nil {
			t.Errorf("rejected %q: %v", input, err)
		}
	}
}
