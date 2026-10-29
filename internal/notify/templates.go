package notify

import (
	"bytes"
	"errors"
	"html/template"
)

type identityMailView struct {
	TitleZH, TitleEN   string
	IntroZH, IntroEN   string
	ActionZH, ActionEN string
	ButtonZH, ButtonEN string
	Link, Request      string
}

type verificationMailView struct{ Code string }

func renderIdentityMail(view identityMailView) (string, error) {
	return renderMail(identityMailTemplate, view)
}

func renderVerificationMail(view verificationMailView) (string, error) {
	return renderMail(verificationMailTemplate, view)
}

func renderMail(source *template.Template, view any) (string, error) {
	var body bytes.Buffer
	if err := source.Execute(&body, view); err != nil {
		return "", errors.New("identity email rendering failed")
	}
	return body.String(), nil
}

const mailStart = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><title>UniHub 归一</title></head>
<body style="margin:0;padding:0;background-color:#f5f7f8;color:#172b33;font-family:Arial,'PingFang SC','Microsoft YaHei',sans-serif;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%;background-color:#f5f7f8;"><tr><td align="center" style="padding:32px 16px;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%;max-width:600px;"><tr><td style="padding:0 8px 20px;font-size:20px;font-weight:700;letter-spacing:0.4px;color:#087f8c;">归一 <span style="color:#172b33;">UniHub</span><br><span style="font-size:12px;font-weight:400;letter-spacing:0;color:#52676f;">统一身份与账户中心 · Identity &amp; account center</span></td></tr>
<tr><td style="background-color:#ffffff;border:1px solid #dfe7e9;border-top:4px solid #087f8c;border-radius:12px;padding:32px 24px;">
`

const mailEnd = `
</td></tr><tr><td style="padding:20px 8px 0;font-size:12px;line-height:1.7;color:#64777e;">UniHub 归一 · 账户安全通知<br>This is an automated account security message.</td></tr></table>
</td></tr></table></body></html>`

var verificationMailTemplate = template.Must(template.New("verification-mail").Parse(mailStart + `
<div lang="zh-CN">
<p style="margin:0 0 8px;font-size:12px;line-height:1.5;font-weight:700;color:#087f8c;letter-spacing:1px;">邮箱验证 / MAILBOX VERIFICATION</p>
<h1 style="margin:0 0 16px;font-size:26px;line-height:1.35;font-weight:700;">确认你的测试邮箱</h1>
<p style="margin:0 0 24px;font-size:16px;line-height:1.7;">你好，你正在参与 UniHub 密码恢复功能验收。请使用下面的校验码确认此邮箱可以正常收信。</p>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%;background-color:#edf7f7;border:1px solid #c4e4e5;border-radius:8px;"><tr><td style="padding:20px 16px;text-align:center;">
<p style="margin:0 0 10px;font-size:13px;line-height:1.5;color:#405d64;">邮箱校验码 / Verification code</p>
<div dir="ltr" style="font-family:monospace;font-size:32px;font-weight:700;line-height:1.5;letter-spacing:1px;color:#075b63;word-break:break-all;overflow-wrap:anywhere;">{{.Code}}</div>
<p style="margin:10px 0 0;font-size:13px;line-height:1.5;color:#405d64;">20 分钟内有效 · Expires in 20 minutes</p>
</td></tr></table>
<p style="margin:24px 0 12px;font-size:16px;line-height:1.7;">请将校验码回复到<strong>当前 Codex 验收对话</strong>，以继续密码恢复验收。无需直接回复这封邮件。</p>
<p style="margin:0;font-size:14px;line-height:1.7;color:#52676f;">此校验码仅用于本次测试邮箱验证。我们将使用独立临时账号，你的日常账号密码保持不变。若非本人操作，请忽略此邮件。</p>
</div>
<div lang="en" style="border-top:1px solid #e4ebed;margin-top:28px;padding-top:24px;">
<p style="margin:0 0 12px;font-size:12px;line-height:1.5;font-weight:700;color:#087f8c;letter-spacing:1px;">ENGLISH</p>
<h2 style="margin:0 0 12px;font-size:20px;line-height:1.4;">Verify your test mailbox</h2>
<p style="margin:0 0 12px;font-size:15px;line-height:1.7;">Use the code above to verify this mailbox for the UniHub password recovery acceptance. Send it in the <strong>current Codex acceptance conversation</strong>, not as an email reply.</p>
<p style="margin:0;font-size:14px;line-height:1.7;color:#52676f;">The code expires in 20 minutes and is only for this test. A separate temporary account will be used; your everyday account password is unchanged. If you did not request this test, ignore this message.</p>
</div>` + mailEnd))

var identityMailTemplate = template.Must(template.New("identity-mail").Parse(mailStart + `
<div lang="zh-CN">
<p style="margin:0 0 8px;font-size:12px;line-height:1.5;font-weight:700;color:#087f8c;letter-spacing:1px;">账户安全 / ACCOUNT SECURITY</p>
<h1 style="margin:0 0 16px;font-size:26px;line-height:1.35;font-weight:700;">{{.TitleZH}}</h1>
<p style="margin:0 0 20px;font-size:16px;line-height:1.7;">你好，{{.IntroZH}}</p>
<p style="margin:0 0 24px;padding:12px 16px;background-color:#edf7f7;border-left:3px solid #087f8c;font-size:14px;line-height:1.7;color:#405d64;">此链接 <strong>20 分钟内有效</strong>，且只能使用一次。<br>Valid for 20 minutes · One-time use</p>
<table role="presentation" cellspacing="0" cellpadding="0" border="0"><tr><td bgcolor="#087f8c" style="background-color:#087f8c;border-radius:6px;"><a href="{{.Link}}" style="display:inline-block;padding:14px 24px;font-size:16px;line-height:1.5;font-weight:700;text-decoration:none;color:#ffffff;">{{.ButtonZH}} / {{.ButtonEN}}</a></td></tr></table>
<p style="margin:24px 0 12px;font-size:14px;line-height:1.7;color:#52676f;">{{.ActionZH}}</p>
<p style="margin:0 0 8px;font-size:13px;line-height:1.7;color:#52676f;">如果按钮无法打开，请复制下方完整链接到浏览器：<br>If the button does not work, copy the complete link below:</p>
<p style="margin:0;font-size:12px;line-height:1.8;word-break:break-all;overflow-wrap:anywhere;"><a href="{{.Link}}" style="color:#087f8c;text-decoration:underline;word-break:break-all;">{{.Link}}</a></p>
</div>
<div lang="en" style="border-top:1px solid #e4ebed;margin-top:28px;padding-top:24px;">
<p style="margin:0 0 12px;font-size:12px;line-height:1.5;font-weight:700;color:#087f8c;letter-spacing:1px;">ENGLISH</p>
<h2 style="margin:0 0 12px;font-size:20px;line-height:1.4;">{{.TitleEN}}</h2>
<p style="margin:0 0 12px;font-size:15px;line-height:1.7;">{{.IntroEN}}</p>
<p style="margin:0;font-size:14px;line-height:1.7;color:#52676f;">{{.ActionEN}}</p>
</div>
<p style="margin:24px 0 0;padding-top:20px;border-top:1px solid #e4ebed;font-size:12px;line-height:1.7;color:#64777e;word-break:break-all;">请求编号 / Request ID<br>{{.Request}}</p>` + mailEnd))
