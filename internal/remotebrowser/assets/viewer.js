const status = document.getElementById("status");
const protocol = location.protocol === "https:" ? "wss:" : "ws:";
let rfb;
let closed = false;
let failed = false;
const timeout = setTimeout(() => {
  failed = true;
  status.textContent = "Kết nối màn hình quá 20 giây. Mở lại Chrome từ trang quản trị; nếu vẫn lỗi, kiểm tra kết nối server.";
  rfb?.disconnect();
}, 20000);

async function connect() {
  try {
    const { default: RFB } = await import("/browser/view/core/rfb.js");
    if (closed || failed) return;
    rfb = new RFB(document.getElementById("screen"), `${protocol}//${location.host}/browser/view/websockify`);
    rfb.scaleViewport = true;
    rfb.addEventListener("connect", () => {
      if (closed || failed) return;
      clearTimeout(timeout);
      status.textContent = "Đã kết nối";
    });
    rfb.addEventListener("disconnect", () => {
      clearTimeout(timeout);
      if (!failed) status.textContent = "Đã ngắt kết nối. Mở lại Chrome từ trang quản trị nếu phiên đã hết hạn.";
    });
    rfb.addEventListener("securityfailure", () => {
      clearTimeout(timeout);
      failed = true;
      status.textContent = "Không xác minh được kết nối màn hình.";
    });
  } catch (error) {
    clearTimeout(timeout);
    failed = true;
    console.error("Chrome display initialization failed", error);
    status.textContent = "Không tải được màn hình Chrome. Mở lại từ trang quản trị; nếu vẫn lỗi, kiểm tra kết nối server.";
  }
}
void connect();
document.getElementById("clipboard").addEventListener("submit", event => {
  event.preventDefault();
  if (!rfb || closed || failed) return;
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
  closed = true;
  clearTimeout(timeout);
  rfb?.disconnect();
  try {
    const response = await fetch("/browser/logout", {method: "POST", credentials: "same-origin"});
    if (!response.ok) throw new Error("logout failed");
    location.replace("/browser/");
  } catch {
    status.textContent = "Đã ngắt màn hình nhưng chưa đóng được phiên. Thử lại nút Đóng phiên điều khiển.";
  }
});
