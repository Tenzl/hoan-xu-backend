const ticket = new URLSearchParams(location.hash.slice(1)).get("ticket");
history.replaceState(null, "", "/browser/");
const status = document.getElementById("status");
if (!ticket) {
  status.textContent = "Mở Chrome từ mục Phiên và cookie Shopee trong trang quản trị.";
} else {
  fetch("/browser/session", {method: "POST", credentials: "same-origin", headers: {"Content-Type": "application/json"}, body: JSON.stringify({ticket})})
    .then(async response => {
      if (!response.ok) throw new Error(await response.text());
      location.replace("/browser/screen");
    }).catch(error => { status.textContent = error.message || "Không kết nối được Chrome. Mở lại từ trang quản trị."; });
}
