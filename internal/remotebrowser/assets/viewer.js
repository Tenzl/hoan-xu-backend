import RFB from "/browser/view/core/rfb.js";
const status = document.getElementById("status");
const protocol = location.protocol === "https:" ? "wss:" : "ws:";
const rfb = new RFB(document.getElementById("screen"), `${protocol}//${location.host}/browser/view/websockify`);
rfb.scaleViewport = true;
rfb.addEventListener("connect", () => { status.textContent = "Đã kết nối"; });
rfb.addEventListener("disconnect", () => { status.textContent = "Đã ngắt kết nối. Mở lại Chrome từ trang quản trị nếu phiên đã hết hạn."; });
rfb.addEventListener("securityfailure", () => { status.textContent = "Không xác minh được kết nối màn hình."; });
document.getElementById("clipboard").addEventListener("submit", event => {
  event.preventDefault();
  const input = document.getElementById("paste");
  rfb.clipboardPasteFrom(input.value);
  input.value = "";
  rfb.focus();
  rfb.sendKey(0xffe3, "ControlLeft", true);
  rfb.sendKey(0x76, "KeyV", true);
  rfb.sendKey(0x76, "KeyV", false);
  rfb.sendKey(0xffe3, "ControlLeft", false);
});
document.getElementById("close").addEventListener("click", async () => {
  rfb.disconnect();
  await fetch("/browser/logout", {method: "POST", credentials: "same-origin"});
  location.replace("/browser/");
});
