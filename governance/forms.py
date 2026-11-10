from datetime import timedelta

from django import forms
from django.utils import timezone

from .models import Activity, Asset, Check, Evidence


class AssetForm(forms.ModelForm):
    class Meta:
        model = Asset
        fields = ["name", "system", "category", "business_level", "statutory_category",
                  "classification_basis", "owner_team", "description"]
        help_texts = {"classification_basis": "标记重要数据或核心数据时，填写可核验的行业目录或确认依据。"}
        widgets = {"description": forms.Textarea(attrs={"rows": 3})}

    def clean(self):
        data = super().clean()
        if data.get("statutory_category") in (Asset.StatutoryCategory.IMPORTANT, Asset.StatutoryCategory.CORE):
            if not data.get("classification_basis"):
                self.add_error("classification_basis", "请填写法定分级依据。")
        return data


class ActivityForm(forms.ModelForm):
    class Meta:
        model = Activity
        fields = ["name", "asset", "kind", "purpose", "recipient", "region", "retention_policy"]

    def __init__(self, *args, workspace, **kwargs):
        super().__init__(*args, **kwargs)
        self.fields["asset"].queryset = Asset.objects.filter(workspace=workspace)


class CheckForm(forms.ModelForm):
    class Meta:
        model = Check
        fields = ["requirement", "asset", "owner_team", "note"]
        widgets = {"note": forms.Textarea(attrs={"rows": 4})}

    def __init__(self, *args, workspace, **kwargs):
        super().__init__(*args, **kwargs)
        self.fields["asset"].queryset = Asset.objects.filter(workspace=workspace)


class EvidenceForm(forms.ModelForm):
    class Meta:
        model = Evidence
        fields = ["kind", "source_system", "source_record", "summary", "observed_at"]
        widgets = {
            "summary": forms.Textarea(attrs={"rows": 4}),
            "observed_at": forms.DateTimeInput(
                format="%Y-%m-%dT%H:%M:%S", attrs={"type": "datetime-local", "step": "1"},
            ),
        }
        help_texts = {
            "source_record": "只填客户系统内的记录编号。凭据、链接签名和业务原文留在源系统。",
            "summary": "填写能说明执行结果的脱敏摘要。",
            "observed_at": "时间按北京时间填写。",
        }

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.initial.setdefault("observed_at", timezone.localtime())

    def clean_observed_at(self):
        value = self.cleaned_data["observed_at"]
        if value > timezone.now() + timedelta(minutes=5):
            raise forms.ValidationError("证据时间不能晚于当前时间。")
        return value


class ReviewForm(forms.Form):
    review_revision = forms.IntegerField(min_value=1, widget=forms.HiddenInput)
    decision = forms.ChoiceField(
        label="复核决定", choices=[("verified", "复核通过"), ("changes", "要求整改")],
    )
    note = forms.CharField(
        label="复核意见", max_length=600, widget=forms.Textarea(attrs={"rows": 4}),
        help_text="说明复核方法和结果，或写明整改要求。",
    )
